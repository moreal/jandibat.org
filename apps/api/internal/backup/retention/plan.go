// Package retention plans whole-chain backup retention without performing I/O.
package retention

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math"
	"sort"
	"time"
)

const approvalLifetime = 15 * time.Minute
const minimumRetention = 35 * 24 * time.Hour

// The caller must provide complete, consecutive pages from a single stable
// catalog snapshot. Complete is set only on the final page.
type Catalog struct {
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

// ObjectID is an opaque identifier supplied by the inventory adapter, never
// an S3 key or URL. The adapter must resolve it again during revalidation.
type ObjectVersion struct {
	ObjectID     string
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

// CheckedAt must be at or after the end of the checked chain. An adapter may
// set FileChecked only after SHOW BACKUP ... WITH check_files succeeds.
type VerifiedRecovery struct {
	ChainID     string
	FileChecked bool
	CheckedAt   time.Time
}

type Range struct {
	From    time.Time
	Through time.Time
}

type TargetVersion struct {
	ObjectID  string
	VersionID string
	SizeBytes int64
}

// RetentionPlan is a dry-run value. It contains no deletion instructions,
// credentials, storage keys, or signed URLs. Its slices are newly allocated.
type RetentionPlan struct {
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
	now, cutoff = now.UTC(), cutoff.UTC()
	if catalog.ActiveBackup || catalog.ActiveRestore {
		return refuse(RefusalConcurrent, "backup or restore is active")
	}
	chains, objects, err := normalizeCatalog(catalog)
	if err != nil {
		return RetentionPlan{}, err
	}
	versions, err := normalizeVersions(inventory, objects)
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
	verifiedAt := make(map[string]time.Time, len(verified))
	for _, evidence := range verified {
		if evidence.ChainID == "" || evidence.CheckedAt.IsZero() || evidence.CheckedAt.After(now) {
			return refuse(RefusalUnverified, "invalid recovery evidence")
		}
		if _, exists := verifiedAt[evidence.ChainID]; exists {
			return refuse(RefusalAmbiguous, "duplicate recovery evidence")
		}
		if evidence.FileChecked {
			verifiedAt[evidence.ChainID] = evidence.CheckedAt.UTC()
		} else {
			verifiedAt[evidence.ChainID] = time.Time{}
		}
	}
	knownChains := make(map[string]bool, len(chains))
	for _, chain := range chains {
		knownChains[chain.ID] = true
	}
	for chainID := range verifiedAt {
		if !knownChains[chainID] {
			return refuse(RefusalAmbiguous, "evidence names unknown chain")
		}
	}

	plan := RetentionPlan{Targets: []TargetVersion{}, RetiredChains: []string{}, ProtectedChains: []string{}}
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
	if coveredThrough.Before(now) {
		return refuse(RefusalCoverage, "protected recovery intervals do not reach planning time")
	}
	plan.Coverage = Range{From: protected[0].Full.At, Through: coveredThrough}

	// A later, fully checked chain must exist before an older whole chain can
	// be selected. Evidence for an earlier state of that later chain is stale.
	for _, chain := range retired {
		newerChecked := false
		for _, successor := range protected {
			if successor.Full.At.After(chain.Through) && !verifiedAt[successor.ID].Before(successor.Through) {
				newerChecked = true
				break
			}
		}
		if !newerChecked {
			return refuse(RefusalUnverified, "no newer file-checked recovery chain")
		}
	}
	if len(retired) == 0 {
		checked := false
		for _, chain := range protected {
			if !verifiedAt[chain.ID].Before(chain.Through) {
				checked = true
				break
			}
		}
		if !checked {
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
			plan.Targets = append(plan.Targets, TargetVersion{ObjectID: id, VersionID: version.VersionID, SizeBytes: version.SizeBytes})
		}
	}
	sort.Slice(plan.Targets, func(i, j int) bool {
		if plan.Targets[i].ObjectID != plan.Targets[j].ObjectID {
			return plan.Targets[i].ObjectID < plan.Targets[j].ObjectID
		}
		return plan.Targets[i].VersionID < plan.Targets[j].VersionID
	})
	plan.ExpiresAt = now.Add(approvalLifetime)
	plan.CatalogDigest, err = digest(struct {
		Chains   []normalizedChain
		Versions []ObjectVersion
	}{chains, sortedVersions(versions)})
	if err != nil {
		return refuse(RefusalAmbiguous, "catalog cannot be encoded for approval")
	}
	plan.ApprovalHash, err = digest(struct {
		CatalogDigest string
		Cutoff        time.Time
		Now           time.Time
		ExpiresAt     time.Time
		Targets       []TargetVersion
		RetiredChains []string
	}{plan.CatalogDigest, cutoff, now, plan.ExpiresAt, plan.Targets, plan.RetiredChains})
	if err != nil {
		return refuse(RefusalAmbiguous, "plan cannot be encoded for approval")
	}
	return plan, nil
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

func normalizeVersions(inventory VersionInventory, objects map[string]bool) (map[string]ObjectVersion, error) {
	if len(inventory.Pages) == 0 {
		_, err := refuse(RefusalIncomplete, "version inventory has no pages")
		return nil, err
	}
	versions := make(map[string]ObjectVersion)
	expectedCursor := ""
	for i, page := range inventory.Pages {
		if page.Cursor != expectedCursor || page.Complete != (i == len(inventory.Pages)-1) || (page.Complete && page.NextCursor != "") || (!page.Complete && page.NextCursor == "") {
			_, err := refuse(RefusalIncomplete, "version page sequence is incomplete")
			return nil, err
		}
		expectedCursor = page.NextCursor
		for _, version := range page.Versions {
			if !objects[version.ObjectID] || version.VersionID == "" || version.SizeBytes < 0 || !version.Current || version.DeleteMarker {
				_, err := refuse(RefusalAmbiguous, "unexpected or noncurrent object version")
				return nil, err
			}
			if _, duplicate := versions[version.ObjectID]; duplicate {
				_, err := refuse(RefusalAmbiguous, "multiple versions for catalog object")
				return nil, err
			}
			version.LockUntil = version.LockUntil.UTC()
			versions[version.ObjectID] = version
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
