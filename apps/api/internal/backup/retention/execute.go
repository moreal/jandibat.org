package retention

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"sync"
	"time"
)

// Lease is implemented by a Kubernetes coordination.k8s.io/v1 adapter. Its
// Acquire must use resourceVersion CAS and MUST NOT steal an expired holder.
// Recovery of an uncertain holder is a separate, audited operator operation.
type Lease interface {
	Acquire(context.Context, string, time.Duration) (resourceVersion string, err error)
	Renew(context.Context, string, string) (newResourceVersion string, err error)
	Release(context.Context, string, string) error
}

type ExecuteRequest struct {
	Plan           RetentionPlan
	ApprovalHash   string
	Cutoff         time.Time
	Verified       []VerifiedRecovery
	HolderIdentity string
	JournalPath    string
	Now            time.Time
}

// External adapters must be scoped to read-only Cockroach inspection and
// exact-version S3 deletion. Nil adapters always fail closed.
type ExecuteDependencies struct {
	Catalog       func(context.Context) (Catalog, error)
	Inventory     func(context.Context) (VersionInventory, error)
	ActiveJobs    func(context.Context) (bool, error)
	Clock         func() time.Time
	Lease         Lease
	DeleteVersion func(context.Context, TargetVersion) error
}

type ExecuteResult struct{ Deleted []TargetVersion }

type durableJournal struct {
	Format        int
	ApprovalHash  string
	CatalogDigest string
	Inventory     []ObjectVersion
	Completed     []TargetVersion
	InFlight      *TargetVersion
}

const (
	leaseDuration = 30 * time.Second
	leaseRenewal  = 10 * time.Second
	deleteTimeout = 10 * time.Second
)

// Execute never deletes an object without a current exact inventory and
// approval. Any uncertain result leaves both the Lease and journal for an
// explicit, separately reviewed operator recovery.
func Execute(ctx context.Context, request ExecuteRequest, deps ExecuteDependencies) (ExecuteResult, error) {
	var empty ExecuteResult
	if ctx == nil || request.ApprovalHash == "" || request.ApprovalHash != request.Plan.ApprovalHash ||
		request.Now.IsZero() || !request.Now.Before(request.Plan.ExpiresAt) ||
		request.Cutoff.IsZero() || request.HolderIdentity == "" ||
		request.JournalPath == "" || !filepath.IsAbs(request.JournalPath) ||
		deps.Catalog == nil || deps.Inventory == nil || deps.ActiveJobs == nil || deps.Clock == nil ||
		deps.Lease == nil || deps.DeleteVersion == nil || len(request.Plan.Targets) == 0 {
		return empty, errors.New("retention execution prerequisites absent")
	}
	if err := ctx.Err(); err != nil {
		return empty, err
	}
	journal, exists, err := readJournal(request.JournalPath)
	if err != nil {
		return empty, err
	}
	if !exists {
		if _, err := os.Stat(request.JournalPath + ".guard"); err == nil {
			return empty, errors.New("retention journal missing after prior run: operator recovery required")
		} else if !errors.Is(err, os.ErrNotExist) {
			return empty, errors.New("retention journal guard cannot be inspected")
		}
	} else if journal.Format != 1 || journal.ApprovalHash != request.ApprovalHash || journal.CatalogDigest != request.Plan.CatalogDigest || journal.InFlight != nil {
		return empty, errors.New("retention journal mismatch or uncertain delete: operator recovery required")
	}
	// The lease adapter, not the executor, owns the non-stealable CAS invariant.
	token, err := deps.Lease.Acquire(ctx, request.HolderIdentity, leaseDuration)
	if err != nil {
		return empty, errors.New("retention lease unavailable")
	}
	if token == "" {
		return empty, errors.New("retention lease returned no resourceVersion")
	}
	// No defer Release: a failed/uncertain operation intentionally leaves the
	// holder in place, including when its TTL has elapsed.
	result := ExecuteResult{Deleted: make([]TargetVersion, 0, len(request.Plan.Targets))}
	for {
		if err := ctx.Err(); err != nil {
			return empty, err
		}
		currentTime := deps.Clock()
		if currentTime.IsZero() || !currentTime.Before(request.Plan.ExpiresAt) {
			return empty, errors.New("retention approval expired")
		}
		token, err = deps.Lease.Renew(ctx, request.HolderIdentity, token)
		if err != nil || token == "" {
			return empty, errors.New("retention lease renewal lost: operator recovery required")
		}
		if active, err := deps.ActiveJobs(ctx); err != nil || active {
			return empty, errors.New("retention active-job check unavailable or active")
		}
		if err := ctx.Err(); err != nil {
			return empty, err
		}
		catalog, err := deps.Catalog(ctx)
		if err != nil {
			return empty, errors.New("retention catalog unavailable")
		}
		inventory, err := deps.Inventory(ctx)
		if err != nil {
			return empty, errors.New("retention version inventory unavailable")
		}
		original, err := validateSnapshot(request, currentTime, catalog, inventory, journal, exists)
		if err != nil {
			return empty, err
		}
		if !exists {
			journal = durableJournal{Format: 1, ApprovalHash: request.ApprovalHash, CatalogDigest: request.Plan.CatalogDigest, Inventory: original, Completed: []TargetVersion{}}
			if err := createGuard(request.JournalPath + ".guard"); err != nil {
				return empty, err
			}
			if err := writeJournal(request.JournalPath, journal); err != nil {
				return empty, err
			}
			exists = true
		}
		if len(journal.Completed) == len(request.Plan.Targets) {
			break
		}
		target := request.Plan.Targets[len(journal.Completed)]
		journal.InFlight = &target
		if err := writeJournal(request.JournalPath, journal); err != nil {
			return empty, err
		}
		// Preflight and fsync can outlive the lease duration. CAS-renew only
		// after both complete, immediately before the irreversible call.
		if err := ctx.Err(); err != nil {
			return empty, err
		}
		if now := deps.Clock(); now.IsZero() || !now.Before(request.Plan.ExpiresAt) {
			return empty, errors.New("retention approval expired before deletion")
		}
		token, err = deps.Lease.Renew(ctx, request.HolderIdentity, token)
		if err != nil || token == "" {
			return empty, errors.New("retention lease lost before deletion: operator recovery required")
		}
		if active, err := deps.ActiveJobs(ctx); err != nil || active {
			return empty, errors.New("retention active-job state changed before deletion: operator recovery required")
		}
		if err := ctx.Err(); err != nil {
			return empty, err
		}
		if err := deleteWithRenewal(ctx, deps, request.HolderIdentity, &token, target); err != nil {
			return empty, errors.New("retention delete unconfirmed: operator recovery required")
		}
		// A nil S3 return alone is not evidence that the exact version has
		// disappeared. Keep InFlight until a complete fresh listing agrees.
		postDelete, err := deps.Inventory(ctx)
		if err != nil {
			return empty, errors.New("retention post-delete inventory unavailable: operator recovery required")
		}
		remaining, err := flatVersions(postDelete)
		if err != nil {
			return empty, errors.New("retention post-delete inventory incomplete: operator recovery required")
		}
		expectedRemaining := make([]ObjectVersion, 0, len(journal.Inventory))
		completed := make(map[string]bool, len(journal.Completed)+1)
		for _, done := range journal.Completed {
			completed[done.Key+"\x00"+done.VersionID] = true
		}
		completed[target.Key+"\x00"+target.VersionID] = true
		for _, object := range journal.Inventory {
			if !completed[object.Key+"\x00"+object.VersionID] {
				expectedRemaining = append(expectedRemaining, object)
			}
		}
		if !equalVersions(expectedRemaining, remaining) {
			return empty, errors.New("retention exact version deletion unconfirmed: operator recovery required")
		}
		journal.InFlight = nil
		journal.Completed = append(journal.Completed, target)
		if err := writeJournal(request.JournalPath, journal); err != nil {
			return empty, errors.New("retention completion journal uncertain: operator recovery required")
		}
		result.Deleted = append(result.Deleted, target)
	}
	if err := deps.Lease.Release(ctx, request.HolderIdentity, token); err != nil {
		return empty, errors.New("retention release unconfirmed: operator recovery required")
	}
	return result, nil
}

func validateSnapshot(request ExecuteRequest, currentTime time.Time, catalog Catalog, current VersionInventory, journal durableJournal, exists bool) ([]ObjectVersion, error) {
	// Validate actual pagination before any reconstruction from the journal.
	currentObjects, err := flatVersions(current)
	if err != nil {
		return nil, err
	}
	original := currentObjects
	if exists {
		original = journal.Inventory
		if len(original) == 0 {
			return nil, errors.New("retention journal inventory absent")
		}
		expected := make([]ObjectVersion, 0, len(original))
		completed := make(map[string]bool, len(journal.Completed))
		for i, target := range journal.Completed {
			if i >= len(request.Plan.Targets) || target != request.Plan.Targets[i] {
				return nil, errors.New("retention journal completion order mismatch")
			}
			completed[target.Key+"\x00"+target.VersionID] = true
		}
		for _, object := range original {
			if !completed[object.Key+"\x00"+object.VersionID] {
				expected = append(expected, object)
			}
		}
		if !equalVersions(expected, currentObjects) {
			return nil, errors.New("retention listing changed since approval")
		}
	}
	approvedAt := request.Plan.ExpiresAt.Add(-approvalLifetime)
	validated, err := Plan(catalog, VersionInventory{Pages: []VersionPage{{Complete: true, Versions: original}}}, request.Verified, request.Cutoff, approvedAt)
	if err != nil {
		return nil, fmt.Errorf("retention approved plan revalidation failed: %w", err)
	}
	if validated.ApprovalHash != request.ApprovalHash || validated.CatalogDigest != request.Plan.CatalogDigest || !reflect.DeepEqual(validated.Targets, request.Plan.Targets) || !reflect.DeepEqual(validated.RetiredChains, request.Plan.RetiredChains) {
		return nil, errors.New("retention approved plan drift")
	}
	// The original hash is stable, while this second evaluation enforces the
	// moving 35-day window and live lock/evidence state at execution time.
	freshCutoff := currentTime.Add(-minimumRetention)
	fresh, err := Plan(catalog, VersionInventory{Pages: []VersionPage{{Complete: true, Versions: original}}}, request.Verified, freshCutoff, currentTime)
	if err != nil {
		return nil, fmt.Errorf("retention current safety check failed: %w", err)
	}
	if fresh.CatalogDigest != request.Plan.CatalogDigest || !reflect.DeepEqual(fresh.Targets, request.Plan.Targets) {
		return nil, errors.New("retention current target drift")
	}
	return original, nil
}

func flatVersions(inventory VersionInventory) ([]ObjectVersion, error) {
	if len(inventory.Pages) == 0 {
		return nil, errors.New("retention version inventory empty")
	}
	var out []ObjectVersion
	expected := ""
	for i, page := range inventory.Pages {
		if page.Cursor != expected || page.Complete != (i == len(inventory.Pages)-1) || (page.Complete && page.NextCursor != "") || (!page.Complete && page.NextCursor == "") {
			return nil, errors.New("retention version inventory pagination incomplete")
		}
		expected = page.NextCursor
		out = append(out, page.Versions...)
	}
	return out, nil
}

func equalVersions(a, b []ObjectVersion) bool {
	clone := func(in []ObjectVersion) []ObjectVersion {
		out := append([]ObjectVersion(nil), in...)
		sort.Slice(out, func(i, j int) bool {
			if out[i].Key != out[j].Key {
				return out[i].Key < out[j].Key
			}
			return out[i].VersionID < out[j].VersionID
		})
		return out
	}
	return reflect.DeepEqual(clone(a), clone(b))
}

func deleteWithRenewal(ctx context.Context, deps ExecuteDependencies, holder string, token *string, target TargetVersion) error {
	deleteCtx, cancel := context.WithTimeout(ctx, deleteTimeout)
	defer cancel()
	var mu sync.Mutex
	var renewalErr error
	done := make(chan struct{})
	stopped := make(chan struct{})
	go func() {
		defer close(stopped)
		ticker := time.NewTicker(leaseRenewal)
		defer ticker.Stop()
		for {
			select {
			case <-done:
				return
			case <-deleteCtx.Done():
				return
			case <-ticker.C:
				mu.Lock()
				next, err := deps.Lease.Renew(deleteCtx, holder, *token)
				if err != nil || next == "" {
					renewalErr = errors.New("lease renewal lost")
					cancel()
				} else {
					*token = next
				}
				mu.Unlock()
			}
		}
	}()
	err := deps.DeleteVersion(deleteCtx, target)
	close(done)
	<-stopped
	mu.Lock()
	defer mu.Unlock()
	if err != nil || renewalErr != nil || deleteCtx.Err() != nil {
		return errors.New("version delete or lease renewal uncertain")
	}
	return nil
}

func readJournal(path string) (durableJournal, bool, error) {
	var j durableJournal
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return j, false, nil
	}
	if err != nil {
		return j, false, errors.New("retention journal unreadable")
	}
	if len(data) > 16<<20 {
		return j, false, errors.New("retention journal oversized")
	}
	if err = json.Unmarshal(data, &j); err != nil {
		return j, false, errors.New("retention journal malformed")
	}
	return j, true, nil
}

func createGuard(path string) error {
	if strings.Contains(path, "\x00") {
		return errors.New("retention journal path invalid")
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return errors.New("retention guard already present or cannot be written")
	}
	if err = f.Sync(); err != nil {
		f.Close()
		return errors.New("retention guard sync failed")
	}
	if err = f.Close(); err != nil {
		return errors.New("retention guard close failed")
	}
	return syncDir(filepath.Dir(path))
}

func writeJournal(path string, j durableJournal) error {
	data, err := json.Marshal(j)
	if err != nil {
		return errors.New("retention journal encode failed")
	}
	f, err := os.CreateTemp(filepath.Dir(path), ".retention-journal-*")
	if err != nil {
		return errors.New("retention journal temp creation failed")
	}
	defer os.Remove(f.Name())
	if err = f.Chmod(0600); err != nil {
		f.Close()
		return errors.New("retention journal mode failed")
	}
	if _, err = f.Write(data); err != nil {
		f.Close()
		return errors.New("retention journal write failed")
	}
	if err = f.Sync(); err != nil {
		f.Close()
		return errors.New("retention journal sync failed")
	}
	if err = f.Close(); err != nil {
		return errors.New("retention journal close failed")
	}
	if err = os.Rename(f.Name(), path); err != nil {
		return errors.New("retention journal rename failed")
	}
	return syncDir(filepath.Dir(path))
}

func syncDir(path string) error {
	dir, err := os.Open(path)
	if err != nil {
		return errors.New("retention journal directory unavailable")
	}
	defer dir.Close()
	if err := dir.Sync(); err != nil {
		return errors.New("retention journal directory sync failed")
	}
	return nil
}
