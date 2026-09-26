package retention

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

type fakeLease struct {
	busy, lost                 bool
	acquires, releases, renews int
	acquireErr                 error
}

func (l *fakeLease) Acquire(_ context.Context, holder string, duration time.Duration) (string, error) {
	l.acquires++
	if l.acquireErr != nil {
		return "", l.acquireErr
	}
	if holder == "" || duration != 30*time.Second || l.busy {
		return "", errors.New("lease unavailable")
	}
	l.busy = true
	return "resource-version-1", nil
}
func (l *fakeLease) Renew(_ context.Context, holder, token string) (string, error) {
	l.renews++
	if l.lost || !l.busy || holder == "" || token == "" {
		return "", errors.New("renewal failed")
	}
	return "resource-version-2", nil
}
func (l *fakeLease) Release(_ context.Context, holder, token string) error {
	l.releases++
	if holder == "" || token == "" {
		return errors.New("invalid release")
	}
	l.busy = false
	return nil
}

type executionFixture struct {
	request   ExecuteRequest
	deps      ExecuteDependencies
	lease     *fakeLease
	deleted   []TargetVersion
	catalog   Catalog
	inventory VersionInventory
	active    bool
}

func newExecutionFixture(t *testing.T) *executionFixture {
	t.Helper()
	catalog, inventory, verified := fixture()
	plan, err := Plan(catalog, inventory, verified, days(-35), testNow)
	if err != nil {
		t.Fatal(err)
	}
	f := &executionFixture{catalog: catalog, inventory: inventory, lease: &fakeLease{}}
	f.request = ExecuteRequest{Plan: plan, ApprovalHash: plan.ApprovalHash, Cutoff: days(-35), Verified: verified, HolderIdentity: "retention-job-1", JournalPath: filepath.Join(t.TempDir(), "journal.json"), Now: testNow.Add(time.Minute)}
	f.deps = ExecuteDependencies{
		Catalog:    func(context.Context) (Catalog, error) { return f.catalog, nil },
		Inventory:  func(context.Context) (VersionInventory, error) { return f.inventory, nil },
		ActiveJobs: func(context.Context) (bool, error) { return f.active, nil },
		Clock:      func() time.Time { return f.request.Now },
		Lease:      f.lease,
		DeleteVersion: func(_ context.Context, target TargetVersion) error {
			f.deleted = append(f.deleted, target)
			for i, object := range f.inventory.Pages[0].Versions {
				if object.Key == target.Key && object.VersionID == target.VersionID {
					f.inventory.Pages[0].Versions = append(f.inventory.Pages[0].Versions[:i], f.inventory.Pages[0].Versions[i+1:]...)
					return nil
				}
			}
			return errors.New("exact version missing")
		},
	}
	return f
}

func TestExecuteDeletesOnlyApprovedExactVersions(t *testing.T) {
	f := newExecutionFixture(t)
	result, err := Execute(context.Background(), f.request, f.deps)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(f.deleted, f.request.Plan.Targets) || !reflect.DeepEqual(result.Deleted, f.request.Plan.Targets) {
		t.Fatalf("deleted=%v result=%v", f.deleted, result.Deleted)
	}
	if f.lease.releases != 1 || f.lease.busy {
		t.Fatalf("lease not released after durable completion: %+v", f.lease)
	}
	if _, err := os.Stat(f.request.JournalPath); err != nil {
		t.Fatalf("journal not durable: %v", err)
	}
}

func TestExecuteRefusesBeforeDeleteForMissingOrStaleApproval(t *testing.T) {
	for _, mutate := range []struct {
		name  string
		apply func(*executionFixture)
	}{
		{"missing approval", func(f *executionFixture) { f.request.ApprovalHash = "" }},
		{"wrong approval", func(f *executionFixture) { f.request.ApprovalHash = "other" }},
		{"expired", func(f *executionFixture) { f.request.Now = f.request.Plan.ExpiresAt }},
		{"missing adapter", func(f *executionFixture) { f.deps.ActiveJobs = nil }},
		{"missing clock", func(f *executionFixture) { f.deps.Clock = nil }},
	} {
		t.Run(mutate.name, func(t *testing.T) {
			f := newExecutionFixture(t)
			mutate.apply(f)
			if _, err := Execute(context.Background(), f.request, f.deps); err == nil {
				t.Fatal("unsafe execution accepted")
			}
			if len(f.deleted) != 0 || f.lease.acquires != 0 {
				t.Fatalf("side effect before validation: %+v", f)
			}
		})
	}
}

func TestExecuteRefusesChangedOrIncompleteInventoryAndActiveJob(t *testing.T) {
	for _, mutate := range []struct {
		name  string
		apply func(*executionFixture)
	}{
		{"changed version", func(f *executionFixture) { f.inventory.Pages[0].Versions[0].VersionID = "different" }},
		{"incomplete page", func(f *executionFixture) {
			f.inventory.Pages[0].Complete = false
			f.inventory.Pages[0].NextCursor = "missing"
		}},
		{"active job", func(f *executionFixture) { f.active = true }},
		{"locked version", func(f *executionFixture) { f.inventory.Pages[0].Versions[0].LockUntil = testNow.Add(time.Hour) }},
	} {
		t.Run(mutate.name, func(t *testing.T) {
			f := newExecutionFixture(t)
			mutate.apply(f)
			if _, err := Execute(context.Background(), f.request, f.deps); err == nil {
				t.Fatal("unsafe execution accepted")
			}
			if len(f.deleted) != 0 {
				t.Fatal("delete occurred")
			}
		})
	}
}

func TestExecuteLeavesLeaseAndJournalOnUncertainDelete(t *testing.T) {
	f := newExecutionFixture(t)
	f.deps.DeleteVersion = func(context.Context, TargetVersion) error { return errors.New("timeout: deletion outcome unknown") }
	if _, err := Execute(context.Background(), f.request, f.deps); err == nil {
		t.Fatal("uncertain deletion accepted")
	}
	if f.lease.releases != 0 || !f.lease.busy {
		t.Fatalf("uncertain lease released: %+v", f.lease)
	}
	if _, err := os.Stat(f.request.JournalPath); err != nil {
		t.Fatalf("journal lost: %v", err)
	}
	if _, err := Execute(context.Background(), f.request, f.deps); err == nil {
		t.Fatal("automatic restart accepted uncertain deletion")
	}
}

func TestExecuteStopsBeforeNextVersionWhenApprovalExpiresDuringDelete(t *testing.T) {
	f := newExecutionFixture(t)
	first := f.deps.DeleteVersion
	f.deps.DeleteVersion = func(ctx context.Context, target TargetVersion) error {
		if err := first(ctx, target); err != nil {
			return err
		}
		f.request.Now = f.request.Plan.ExpiresAt
		return nil
	}
	if _, err := Execute(context.Background(), f.request, f.deps); err == nil {
		t.Fatal("expiry during execution accepted")
	}
	if len(f.deleted) != 1 || f.lease.releases != 0 {
		t.Fatalf("unsafe continuation: deleted=%d lease=%+v", len(f.deleted), f.lease)
	}
}

func TestExecuteLeavesHeldLeaseOnRenewalLoss(t *testing.T) {
	f := newExecutionFixture(t)
	first := f.deps.DeleteVersion
	f.deps.DeleteVersion = func(ctx context.Context, target TargetVersion) error {
		if err := first(ctx, target); err != nil {
			return err
		}
		f.lease.lost = true
		return nil
	}
	if _, err := Execute(context.Background(), f.request, f.deps); err == nil {
		t.Fatal("renewal loss accepted")
	}
	if len(f.deleted) != 1 || f.lease.releases != 0 {
		t.Fatalf("unsafe continuation: deleted=%d lease=%+v", len(f.deleted), f.lease)
	}
}

func TestExecuteRechecksLeaseAfterSlowPreflightBeforeDelete(t *testing.T) {
	f := newExecutionFixture(t)
	f.deps.ActiveJobs = func(context.Context) (bool, error) {
		f.lease.lost = true // a stalled preflight outlived its CAS holder.
		return false, nil
	}
	if _, err := Execute(context.Background(), f.request, f.deps); err == nil {
		t.Fatal("lost lease accepted after preflight")
	}
	if len(f.deleted) != 0 || f.lease.releases != 0 {
		t.Fatalf("delete or release after lost lease: %+v deleted=%v", f.lease, f.deleted)
	}
}

func TestExecuteRechecksActiveJobsImmediatelyBeforeDelete(t *testing.T) {
	f := newExecutionFixture(t)
	checks := 0
	f.deps.ActiveJobs = func(context.Context) (bool, error) {
		checks++
		return checks >= 2, nil // a manual backup starts while inventory is read.
	}
	if _, err := Execute(context.Background(), f.request, f.deps); err == nil {
		t.Fatal("newly active backup accepted")
	}
	if len(f.deleted) != 0 || f.lease.releases != 0 {
		t.Fatalf("delete or release under active job: deleted=%v lease=%+v", f.deleted, f.lease)
	}
}

func TestExecuteRefusesUnconfirmedFinalExactVersionDelete(t *testing.T) {
	f := newExecutionFixture(t)
	first := f.deps.DeleteVersion
	deletes := 0
	f.deps.DeleteVersion = func(ctx context.Context, target TargetVersion) error {
		deletes++
		if deletes == 2 {
			return nil
		} // S3 adapter reports success but exact version survives.
		return first(ctx, target)
	}
	if _, err := Execute(context.Background(), f.request, f.deps); err == nil {
		t.Fatal("unconfirmed final version accepted")
	}
	if f.lease.releases != 0 || !f.lease.busy {
		t.Fatalf("lease released after unconfirmed delete: %+v", f.lease)
	}
	journal, _, err := readJournal(f.request.JournalPath)
	if err != nil {
		t.Fatal(err)
	}
	if len(journal.Completed) != 1 || journal.InFlight == nil || *journal.InFlight != f.request.Plan.Targets[1] {
		t.Fatalf("unverified version marked complete: %+v", journal)
	}
}

func TestExecuteRedactsLeaseAcquireFailure(t *testing.T) {
	f := newExecutionFixture(t)
	f.lease.acquireErr = errors.New("secret-sentinel-in-adapter-error")
	_, err := Execute(context.Background(), f.request, f.deps)
	if err == nil || strings.Contains(err.Error(), "secret-sentinel") {
		t.Fatalf("lease error leaked sensitive data: %v", err)
	}
	if len(f.deleted) != 0 {
		t.Fatal("deleted without lease")
	}
}

func TestExecuteNeverTakesExpiredOrHeldLease(t *testing.T) {
	f := newExecutionFixture(t)
	f.lease.busy = true
	if _, err := Execute(context.Background(), f.request, f.deps); err == nil {
		t.Fatal("held lease stolen")
	}
	if len(f.deleted) != 0 || f.lease.releases != 0 {
		t.Fatal("side effect under held lease")
	}
}

func TestExecuteBlocksLostPartialJournal(t *testing.T) {
	f := newExecutionFixture(t)
	f.deps.DeleteVersion = func(_ context.Context, target TargetVersion) error {
		f.deleted = append(f.deleted, target)
		if len(f.deleted) == 2 {
			return errors.New("network uncertainty")
		}
		return nil
	}
	if _, err := Execute(context.Background(), f.request, f.deps); err == nil {
		t.Fatal("partial failure accepted")
	}
	if err := os.Remove(f.request.JournalPath); err != nil {
		t.Fatal(err)
	}
	f.lease.busy = false // operator must recover Lease separately; journal loss still blocks.
	if _, err := Execute(context.Background(), f.request, f.deps); err == nil {
		t.Fatal("missing partial journal accepted")
	}
}

func TestExecuteResumesOnlyRemainingVersionsAfterExplicitLeaseRecovery(t *testing.T) {
	f := newExecutionFixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	first := f.deps.DeleteVersion
	checks := 0
	f.deps.ActiveJobs = func(context.Context) (bool, error) {
		checks++
		if checks == 3 {
			cancel()
		} // first delete has been durably journaled.
		return false, nil
	}
	f.deps.DeleteVersion = first
	if _, err := Execute(ctx, f.request, f.deps); err == nil {
		t.Fatal("cancelled execution accepted")
	}
	if len(f.deleted) != 1 || f.lease.releases != 0 {
		t.Fatalf("partial state: deleted=%d releases=%d", len(f.deleted), f.lease.releases)
	}
	// A separately authorized operator recovery has established terminal jobs,
	// quarantined outstanding requests and cleared the non-stealable Lease.
	f.lease.busy = false
	f.deps.DeleteVersion = first
	result, err := Execute(context.Background(), f.request, f.deps)
	if err != nil {
		t.Fatal(err)
	}
	if len(f.deleted) != 2 || len(result.Deleted) != 1 || result.Deleted[0] != f.request.Plan.Targets[1] {
		t.Fatalf("resume deleted wrong versions: all=%v resumed=%v", f.deleted, result.Deleted)
	}
}
