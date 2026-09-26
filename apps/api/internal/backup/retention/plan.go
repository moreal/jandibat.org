// Package retention plans whole-chain backup retention without performing I/O.
package retention

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math"
	"sort"
	"strings"
	"time"
)

const approvalLifetime = 15 * time.Minute
const minimumRetention = 35 * 24 * time.Hour
const maximumRecoveryLag = time.Hour

// StorageNamespace names the exact S3 target scope. Prefix is a nonempty
// slash-terminated key prefix, not a URL or a credential.
type StorageNamespace struct {
	Bucket string
	Prefix string
}

// The caller must provide complete, consecutive pages from a single stable
// catalog snapshot. Complete is set only on the final page.
type Catalog struct {
	Namespace     StorageNamespace
	Pages         []CatalogPage
	ActiveBackup  bool
	ActiveRestore bool
}

type CatalogPage struct {
	Cursor     string
	NextCursor string
	Complete   bool
	Chains     []Chain
}

// A chain starts with one full backup. Incrementals must be contiguous and
// ordered; their intervals state the recovery range offered by the chain.
type Chain struct {
	ID           string
	Full         Full
	Incrementals []Incremental
}

type Full struct {
	ID        string
	At        time.Time
	ObjectIDs []string
}

type Incremental struct {
	ID        string
	From      time.Time
	Through   time.Time
	ObjectIDs []string
}

// ObjectID joins a catalog object to its exact S3 key and version. Key is the
// full key within Namespace.Bucket and must be inside Namespace.Prefix.
type ObjectVersion struct {
	ObjectID     string
	Key          string
	VersionID    string
	SizeBytes    int64
	Current      bool
	DeleteMarker bool
	LockUntil    time.Time
}

type VersionInventory struct{ Pages []VersionPage }

type VersionPage struct {
	Cursor     string
	NextCursor string
	Complete   bool
	Versions   []ObjectVersion
}

// CheckedAt must be at or after the end of the checked chain. ManifestDigest
// captures the chain and exact object versions at check_files time; callers
// must retain that digest, not recompute it from a later inventory.
type VerifiedRecovery struct {
	ChainID        string
	FileChecked    bool
	CheckedAt      time.Time
	ManifestDigest string
}

type Range struct {
	From    time.Time
	Through time.Time
}

type TargetVersion struct {
	Bucket    string
	Key       string
	VersionID string
	SizeBytes int64
}

// RetentionPlan is a dry-run value. It contains exact target keys and versions,
// but no deletion instructions, credentials, or signed URLs.
type RetentionPlan struct {
	Namespace            StorageNamespace
	Targets              []TargetVersion
	RetiredChains        []string
	ProtectedChains      []string
	TargetRange          Range
	Coverage             Range
	ExpectedFreedBytes   int64
	StorageOverhangBytes int64
	CatalogDigest        string
	ApprovalHash         string
	ExpiresAt            time.Time
}

type RefusalCode string

const (
	RefusalInvalid    RefusalCode = "invalid_input"
	RefusalIncomplete RefusalCode = "incomplete_inventory"
	RefusalAmbiguous  RefusalCode = "ambiguous_inventory"
	RefusalConcurrent RefusalCode = "concurrent_work"
	RefusalLocked     RefusalCode = "locked_version"
	RefusalCoverage   RefusalCode = "insufficient_coverage"
	RefusalUnverified RefusalCode = "unverified_recovery"
)

type Refusal struct {
	Code   RefusalCode
	Detail string
}

func (r *Refusal) Error() string { return fmt.Sprintf("backup retention: %s: %s", r.Code, r.Detail) }

func refuse(code RefusalCode, detail string) (RetentionPlan, error) {
	return RetentionPlan{}, &Refusal{Code: code, Detail: detail}
}

type normalizedChain struct {
	ID           string
	Full         Full
	Incrementals []Incremental
	Through      time.Time
}

// Plan proposes exact object versions belonging to complete retired chains.
// A refusal always returns a zero plan, so no caller can accidentally use a
// partial target list. Cutoff may be older than 35 days, never newer.
func Plan(catalog Catalog, inventory VersionInventory, verified []VerifiedRecovery, cutoff, now time.Time) (RetentionPlan, error) {
	if now.IsZero() || cutoff.IsZero() || cutoff.After(now.Add(-minimumRetention)) {
		return refuse(RefusalInvalid, "cutoff must preserve at least 35 days")
	}
	if err := validateNamespace(catalog.Namespace); err != nil {
		return RetentionPlan{}, err
	}
	now, cutoff = now.UTC(), cutoff.UTC()
	if catalog.ActiveBackup || catalog.ActiveRestore {
		return refuse(RefusalConcurrent, "backup or restore is active")
	}
	chains, objects, err := normalizeCatalog(catalog)
	if err != nil {
		return RetentionPlan{}, err
	}
	versions, err := normalizeVersions(inventory, objects, catalog.Namespace)
	if err != nil {
		return RetentionPlan{}, err
	}
	if len(chains) == 0 {
		return refuse(RefusalCoverage, "no backup chains")
	}
	for _, chain := range chains {
		if chain.Full.At.After(now) || chain.Through.After(now) {
			return refuse(RefusalAmbiguous, "backup chronology extends beyond planning time")
		}
	}
	evidenceByChain := make(map[string]VerifiedRecovery, len(verified))
	for _, evidence := range verified {
		if evidence.ChainID == "" || evidence.CheckedAt.IsZero() || evidence.CheckedAt.After(now) {
			return refuse(RefusalUnverified, "invalid recovery evidence")
		}
		if _, exists := evidenceByChain[evidence.ChainID]; exists {
			return refuse(RefusalAmbiguous, "duplicate recovery evidence")
		}
		evidenceByChain[evidence.ChainID] = evidence
	}
	knownChains := make(map[string]normalizedChain, len(chains))
	for _, chain := range chains {
		knownChains[chain.ID] = chain
	}
	checked := make(map[string]bool, len(evidenceByChain))
	for chainID, evidence := range evidenceByChain {
		chain, exists := knownChains[chainID]
		if !exists {
			return refuse(RefusalAmbiguous, "evidence names unknown chain")
		}
		manifest, err := chainManifestDigest(catalog.Namespace, chain, versions)
		if err != nil {
			return refuse(RefusalAmbiguous, "chain manifest cannot be encoded")
		}
		checked[chainID] = evidence.FileChecked && !evidence.CheckedAt.Before(chain.Through) && evidence.ManifestDigest == manifest
	}

	plan := RetentionPlan{Namespace: catalog.Namespace, Targets: []TargetVersion{}, RetiredChains: []string{}, ProtectedChains: []string{}}
	var protected []normalizedChain
	var retired []normalizedChain
	for _, chain := range chains {
		if chain.Through.Before(cutoff) {
			retired = append(retired, chain)
			plan.RetiredChains = append(plan.RetiredChains, chain.ID)
		} else {
			protected = append(protected, chain)
			plan.ProtectedChains = append(plan.ProtectedChains, chain.ID)
		}
	}
	if len(protected) == 0 || protected[0].Full.At.After(cutoff) {
		return refuse(RefusalCoverage, "no chain covers the retention cutoff")
	}
	coveredThrough := protected[0].Through
	for _, chain := range protected[1:] {
		if chain.Full.At.After(coveredThrough) {
			return refuse(RefusalCoverage, "gap in protected recovery intervals")
		}
		if chain.Through.After(coveredThrough) {
			coveredThrough = chain.Through
		}
	}
	if coveredThrough.Before(now.Add(-maximumRecoveryLag)) {
		return refuse(RefusalCoverage, "protected recovery watermark exceeds one-hour RPO")
	}
	plan.Coverage = Range{From: protected[0].Full.At, Through: coveredThrough}

	// A later, fully checked chain must exist before an older whole chain can
	// be selected. Evidence for an earlier state of that later chain is stale.
	for _, chain := range retired {
		newerChecked := false
		for _, successor := range protected {
			if successor.Full.At.After(chain.Through) && checked[successor.ID] {
				newerChecked = true
				break
			}
		}
		if !newerChecked {
			return refuse(RefusalUnverified, "no newer file-checked recovery chain")
		}
	}
	if len(retired) == 0 {
		anyChecked := false
		for _, chain := range protected {
			if checked[chain.ID] {
				anyChecked = true
				break
			}
		}
		if !anyChecked {
			return refuse(RefusalUnverified, "no file-checked recovery chain")
		}
	}
	for _, chain := range protected {
		if chain.Full.At.Before(cutoff) {
			for _, id := range chainObjectIDs(chain) {
				var ok bool
				plan.StorageOverhangBytes, ok = addBytes(plan.StorageOverhangBytes, versions[id].SizeBytes)
				if !ok {
					return refuse(RefusalAmbiguous, "byte count overflow")
				}
			}
		}
	}
	for _, chain := range retired {
		if plan.TargetRange.From.IsZero() || chain.Full.At.Before(plan.TargetRange.From) {
			plan.TargetRange.From = chain.Full.At
		}
		if chain.Through.After(plan.TargetRange.Through) {
			plan.TargetRange.Through = chain.Through
		}
		for _, id := range chainObjectIDs(chain) {
			version := versions[id]
			if version.LockUntil.After(now) {
				return refuse(RefusalLocked, "target version has unexpired Object Lock")
			}
			var ok bool
			plan.ExpectedFreedBytes, ok = addBytes(plan.ExpectedFreedBytes, version.SizeBytes)
			if !ok {
				return refuse(RefusalAmbiguous, "byte count overflow")
			}
			plan.Targets = append(plan.Targets, TargetVersion{Bucket: catalog.Namespace.Bucket, Key: version.Key, VersionID: version.VersionID, SizeBytes: version.SizeBytes})
		}
	}
	sort.Slice(plan.Targets, func(i, j int) bool {
		if plan.Targets[i].Bucket != plan.Targets[j].Bucket {
			return plan.Targets[i].Bucket < plan.Targets[j].Bucket
		}
		if plan.Targets[i].Key != plan.Targets[j].Key {
			return plan.Targets[i].Key < plan.Targets[j].Key
		}
		return plan.Targets[i].VersionID < plan.Targets[j].VersionID
	})
	plan.ExpiresAt = now.Add(approvalLifetime)
	plan.CatalogDigest, err = digest(struct {
		Namespace StorageNamespace
		Chains    []normalizedChain
		Versions  []ObjectVersion
	}{catalog.Namespace, chains, sortedVersions(versions)})
	if err != nil {
		return refuse(RefusalAmbiguous, "catalog cannot be encoded for approval")
	}
	plan.ApprovalHash, err = digest(struct {
		Namespace     StorageNamespace
		CatalogDigest string
		Cutoff        time.Time
		Now           time.Time
		ExpiresAt     time.Time
		Targets       []TargetVersion
		RetiredChains []string
	}{plan.Namespace, plan.CatalogDigest, cutoff, now, plan.ExpiresAt, plan.Targets, plan.RetiredChains})
	if err != nil {
		return refuse(RefusalAmbiguous, "plan cannot be encoded for approval")
	}
	return plan, nil
}

// ChainManifestDigest fingerprints the exact checked chain, namespace, and
// object versions. Capture it when check_files succeeds, then pass the saved
// value as VerifiedRecovery.ManifestDigest when planning against a fresh
// inventory. A changed chain or version will invalidate that evidence.
func ChainManifestDigest(catalog Catalog, inventory VersionInventory, chainID string) (string, error) {
	if err := validateNamespace(catalog.Namespace); err != nil {
		return "", err
	}
	chains, objects, err := normalizeCatalog(catalog)
	if err != nil {
		return "", err
	}
	versions, err := normalizeVersions(inventory, objects, catalog.Namespace)
	if err != nil {
		return "", err
	}
	for _, chain := range chains {
		if chain.ID == chainID {
			manifest, err := chainManifestDigest(catalog.Namespace, chain, versions)
			if err != nil {
				_, refusal := refuse(RefusalAmbiguous, "chain manifest cannot be encoded")
				return "", refusal
			}
			return manifest, nil
		}
	}
	_, refusal := refuse(RefusalAmbiguous, "checked chain absent from catalog")
	return "", refusal
}

func chainManifestDigest(namespace StorageNamespace, chain normalizedChain, versions map[string]ObjectVersion) (string, error) {
	bound := make(map[string]ObjectVersion)
	for _, id := range chainObjectIDs(chain) {
		bound[id] = versions[id]
	}
	return digest(struct {
		Namespace StorageNamespace
		Chain     normalizedChain
		Versions  []ObjectVersion
	}{namespace, chain, sortedVersions(bound)})
}

func validateNamespace(namespace StorageNamespace) error {
	if namespace.Bucket == "" || namespace.Bucket != strings.TrimSpace(namespace.Bucket) || strings.ContainsAny(namespace.Bucket, "/\\:@?# ") ||
		namespace.Prefix == "" || !strings.HasSuffix(namespace.Prefix, "/") || strings.HasPrefix(namespace.Prefix, "/") ||
		strings.Contains(namespace.Prefix, "//") || strings.Contains(namespace.Prefix, "../") || strings.Contains(namespace.Prefix, "./") || namespace.Prefix != strings.TrimSpace(namespace.Prefix) {
		_, err := refuse(RefusalInvalid, "noncanonical bucket or prefix")
		return err
	}
	return nil
}

func normalizeCatalog(catalog Catalog) ([]normalizedChain, map[string]bool, error) {
	if len(catalog.Pages) == 0 {
		_, err := refuse(RefusalIncomplete, "catalog has no pages")
		return nil, nil, err
	}
	var chains []normalizedChain
	objects := make(map[string]bool)
	chainIDs := make(map[string]bool)
	segmentIDs := make(map[string]bool)
	expectedCursor := ""
	for i, page := range catalog.Pages {
		if page.Cursor != expectedCursor || page.Complete != (i == len(catalog.Pages)-1) || (page.Complete && page.NextCursor != "") || (!page.Complete && page.NextCursor == "") {
			_, err := refuse(RefusalIncomplete, "catalog page sequence is incomplete")
			return nil, nil, err
		}
		expectedCursor = page.NextCursor
		for _, chain := range page.Chains {
			if chain.ID == "" || chainIDs[chain.ID] || chain.Full.ID == "" || segmentIDs[chain.Full.ID] || chain.Full.At.IsZero() || len(chain.Full.ObjectIDs) == 0 {
				_, err := refuse(RefusalAmbiguous, "invalid or duplicate chain/full")
				return nil, nil, err
			}
			chainIDs[chain.ID], segmentIDs[chain.Full.ID] = true, true
			full := Full{ID: chain.Full.ID, At: chain.Full.At.UTC(), ObjectIDs: append([]string(nil), chain.Full.ObjectIDs...)}
			if err := addObjectIDs(objects, full.ObjectIDs); err != nil {
				return nil, nil, err
			}
			sort.Strings(full.ObjectIDs)
			normalized := normalizedChain{ID: chain.ID, Full: full, Through: full.At, Incrementals: make([]Incremental, 0, len(chain.Incrementals))}
			for _, inc := range chain.Incrementals {
				if inc.ID == "" || segmentIDs[inc.ID] || inc.From.IsZero() || inc.Through.IsZero() || len(inc.ObjectIDs) == 0 || !inc.From.Equal(normalized.Through) || !inc.Through.After(inc.From) {
					_, err := refuse(RefusalAmbiguous, "invalid incremental dependency")
					return nil, nil, err
				}
				segmentIDs[inc.ID] = true
				copyInc := Incremental{ID: inc.ID, From: inc.From.UTC(), Through: inc.Through.UTC(), ObjectIDs: append([]string(nil), inc.ObjectIDs...)}
				if err := addObjectIDs(objects, copyInc.ObjectIDs); err != nil {
					return nil, nil, err
				}
				sort.Strings(copyInc.ObjectIDs)
				normalized.Incrementals = append(normalized.Incrementals, copyInc)
				normalized.Through = copyInc.Through
			}
			chains = append(chains, normalized)
		}
	}
	sort.Slice(chains, func(i, j int) bool {
		if !chains[i].Full.At.Equal(chains[j].Full.At) {
			return chains[i].Full.At.Before(chains[j].Full.At)
		}
		return chains[i].ID < chains[j].ID
	})
	return chains, objects, nil
}

func addObjectIDs(seen map[string]bool, ids []string) error {
	for _, id := range ids {
		if id == "" || seen[id] {
			_, err := refuse(RefusalAmbiguous, "missing or duplicate catalog object")
			return err
		}
		seen[id] = true
	}
	return nil
}

func normalizeVersions(inventory VersionInventory, objects map[string]bool, namespace StorageNamespace) (map[string]ObjectVersion, error) {
	if len(inventory.Pages) == 0 {
		_, err := refuse(RefusalIncomplete, "version inventory has no pages")
		return nil, err
	}
	versions := make(map[string]ObjectVersion)
	keys := make(map[string]bool)
	expectedCursor := ""
	for i, page := range inventory.Pages {
		if page.Cursor != expectedCursor || page.Complete != (i == len(inventory.Pages)-1) || (page.Complete && page.NextCursor != "") || (!page.Complete && page.NextCursor == "") {
			_, err := refuse(RefusalIncomplete, "version page sequence is incomplete")
			return nil, err
		}
		expectedCursor = page.NextCursor
		for _, version := range page.Versions {
			if !objects[version.ObjectID] || version.VersionID == "" || strings.EqualFold(version.VersionID, "null") ||
				version.Key == "" || version.Key == namespace.Prefix || !strings.HasPrefix(version.Key, namespace.Prefix) ||
				version.Key != strings.TrimSpace(version.Key) || version.SizeBytes < 0 || !version.Current || version.DeleteMarker {
				_, err := refuse(RefusalAmbiguous, "unexpected or noncurrent object version")
				return nil, err
			}
			if _, duplicate := versions[version.ObjectID]; duplicate {
				_, err := refuse(RefusalAmbiguous, "multiple versions for catalog object")
				return nil, err
			}
			if keys[version.Key] {
				_, err := refuse(RefusalAmbiguous, "multiple catalog objects share an exact key")
				return nil, err
			}
			version.LockUntil = version.LockUntil.UTC()
			versions[version.ObjectID] = version
			keys[version.Key] = true
		}
	}
	if len(versions) != len(objects) {
		_, err := refuse(RefusalIncomplete, "catalog object absent from version inventory")
		return nil, err
	}
	return versions, nil
}

func chainObjectIDs(chain normalizedChain) []string {
	ids := append([]string(nil), chain.Full.ObjectIDs...)
	for _, inc := range chain.Incrementals {
		ids = append(ids, inc.ObjectIDs...)
	}
	return ids
}

func sortedVersions(values map[string]ObjectVersion) []ObjectVersion {
	versions := make([]ObjectVersion, 0, len(values))
	for _, version := range values {
		versions = append(versions, version)
	}
	sort.Slice(versions, func(i, j int) bool {
		if versions[i].ObjectID != versions[j].ObjectID {
			return versions[i].ObjectID < versions[j].ObjectID
		}
		return versions[i].VersionID < versions[j].VersionID
	})
	return versions
}

func addBytes(total, next int64) (int64, bool) {
	if next > math.MaxInt64-total {
		return 0, false
	}
	return total + next, true
}

func digest(value any) (string, error) {
	encoded, err := json.Marshal(value)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(encoded)
	return hex.EncodeToString(sum[:]), nil
}
