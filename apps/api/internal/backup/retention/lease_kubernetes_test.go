package retention

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"testing"
	"time"
)

type fakeKubernetesLeaseAPI struct {
	record            KubernetesLeaseRecord
	gets, updates     int
	getErr, updateErr error
	beforeUpdate      func()
}

func (f *fakeKubernetesLeaseAPI) Get(_ context.Context, namespace, name string) (KubernetesLeaseRecord, error) {
	f.gets++
	if f.getErr != nil {
		return KubernetesLeaseRecord{}, f.getErr
	}
	if namespace != f.record.Namespace || name != f.record.Name {
		return KubernetesLeaseRecord{}, errors.New("wrong resource")
	}
	return f.record, nil
}
func (f *fakeKubernetesLeaseAPI) Update(_ context.Context, next KubernetesLeaseRecord) (KubernetesLeaseRecord, error) {
	f.updates++
	if f.beforeUpdate != nil {
		f.beforeUpdate()
	}
	if f.updateErr != nil {
		return KubernetesLeaseRecord{}, f.updateErr
	}
	if next.Namespace != f.record.Namespace || next.Name != f.record.Name || next.ResourceVersion != f.record.ResourceVersion {
		return KubernetesLeaseRecord{}, errors.New("resourceVersion conflict")
	}
	version, err := strconv.Atoi(f.record.ResourceVersion)
	if err != nil {
		return KubernetesLeaseRecord{}, err
	}
	next.ResourceVersion = strconv.Itoa(version + 1)
	f.record = next
	return next, nil
}

func newLeaseAdapter(t *testing.T) (*KubernetesLease, *fakeKubernetesLeaseAPI, *time.Time) {
	t.Helper()
	now := time.Date(2026, 9, 26, 12, 0, 0, 0, time.UTC)
	api := &fakeKubernetesLeaseAPI{record: KubernetesLeaseRecord{Namespace: "jandibat", Name: "jandibat-backup-chain-operations", ResourceVersion: "1"}}
	adapter, err := NewKubernetesLease(api, "jandibat", func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	return adapter, api, &now
}

func TestKubernetesLeaseAcquiresPrecreatedExactResourceWithCAS(t *testing.T) {
	adapter, api, now := newLeaseAdapter(t)
	token, err := adapter.Acquire(context.Background(), "retention-job-unique", 30*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if token != "2" || api.record.HolderIdentity != "retention-job-unique" || api.record.LeaseDurationSeconds != 30 || !api.record.RenewTime.Equal(*now) || api.gets != 1 || api.updates != 1 {
		t.Fatalf("unexpected CAS lease: token=%q record=%+v gets=%d updates=%d", token, api.record, api.gets, api.updates)
	}
	if _, err := adapter.Acquire(context.Background(), "second-job", 30*time.Second); err == nil {
		t.Fatal("second holder acquired held lease")
	}
	if api.updates != 1 {
		t.Fatal("held lease was rewritten")
	}
}

func TestKubernetesLeaseNeverStealsExpiredHolder(t *testing.T) {
	adapter, api, now := newLeaseAdapter(t)
	api.record.HolderIdentity = "crashed-restore"
	api.record.LeaseDurationSeconds = 30
	api.record.RenewTime = now.Add(-time.Hour)
	if _, err := adapter.Acquire(context.Background(), "retention-job", 30*time.Second); err == nil {
		t.Fatal("expired restore holder was stolen")
	}
	if api.updates != 0 || api.record.HolderIdentity != "crashed-restore" {
		t.Fatalf("expired holder changed: %+v", api.record)
	}
}

func TestKubernetesLeaseRenewsAndReleasesOnlyOwnCurrentResourceVersion(t *testing.T) {
	adapter, api, now := newLeaseAdapter(t)
	token, err := adapter.Acquire(context.Background(), "retention-job", 30*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	*now = now.Add(10 * time.Second)
	if _, err := adapter.Renew(context.Background(), "other-job", token); err == nil {
		t.Fatal("other holder renewed")
	}
	if _, err := adapter.Renew(context.Background(), "retention-job", "stale"); err == nil {
		t.Fatal("stale resourceVersion renewed")
	}
	next, err := adapter.Renew(context.Background(), "retention-job", token)
	if err != nil {
		t.Fatal(err)
	}
	if next != "3" || !api.record.RenewTime.Equal(*now) || api.record.HolderIdentity != "retention-job" {
		t.Fatalf("bad renewal: token=%s record=%+v", next, api.record)
	}
	if err := adapter.Release(context.Background(), "retention-job", token); err == nil {
		t.Fatal("stale resourceVersion released")
	}
	if err := adapter.Release(context.Background(), "retention-job", next); err != nil {
		t.Fatal(err)
	}
	if api.record.HolderIdentity != "" || api.record.ResourceVersion != "4" {
		t.Fatalf("release not CAS: %+v", api.record)
	}
}

func TestKubernetesLeaseLostOrExpiredRenewalLeavesHolder(t *testing.T) {
	adapter, api, now := newLeaseAdapter(t)
	token, err := adapter.Acquire(context.Background(), "retention-job", 30*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	*now = now.Add(30 * time.Second)
	if _, err := adapter.Renew(context.Background(), "retention-job", token); err == nil {
		t.Fatal("expired own lease renewed")
	}
	if err := adapter.Release(context.Background(), "retention-job", token); err == nil {
		t.Fatal("expired holder released")
	}
	if api.record.HolderIdentity != "retention-job" || api.updates != 1 {
		t.Fatalf("expiry changed lease: %+v", api.record)
	}
}

func TestKubernetesLeaseRefusesClockRegression(t *testing.T) {
	adapter, api, now := newLeaseAdapter(t)
	token, err := adapter.Acquire(context.Background(), "retention-job", 30*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	*now = now.Add(-time.Second)
	if _, err := adapter.Renew(context.Background(), "retention-job", token); err == nil {
		t.Fatal("clock regression renewed lease")
	}
	if api.record.ResourceVersion != "2" || api.record.HolderIdentity != "retention-job" {
		t.Fatalf("clock regression changed lease: %+v", api.record)
	}
}

func TestKubernetesLeaseConflictAndAPIErrorFailClosedWithoutDetails(t *testing.T) {
	adapter, api, _ := newLeaseAdapter(t)
	api.beforeUpdate = func() { api.record.ResourceVersion = "99" }
	if _, err := adapter.Acquire(context.Background(), "retention-job", 30*time.Second); err == nil {
		t.Fatal("CAS conflict accepted")
	}
	if api.record.HolderIdentity != "" {
		t.Fatal("conflict replaced holder")
	}
	api.beforeUpdate = nil
	api.getErr = errors.New("secret-sentinel-in-kube-error")
	if _, err := adapter.Acquire(context.Background(), "retention-job", 30*time.Second); err == nil || strings.Contains(err.Error(), "secret-sentinel") {
		t.Fatalf("API error leaked: %v", err)
	}
}

func TestKubernetesLeaseRejectsInvalidContractAndMissingResource(t *testing.T) {
	adapter, api, _ := newLeaseAdapter(t)
	if _, err := NewKubernetesLease(api, "../wrong", time.Now); err == nil {
		t.Fatal("invalid namespace accepted")
	}
	if _, err := NewKubernetesLease(nil, "jandibat", time.Now); err == nil {
		t.Fatal("nil API accepted")
	}
	if _, err := adapter.Acquire(context.Background(), "", 30*time.Second); err == nil {
		t.Fatal("empty holder accepted")
	}
	if _, err := adapter.Acquire(context.Background(), "holder", time.Minute); err == nil {
		t.Fatal("wrong duration accepted")
	}
	api.getErr = errors.New("not found")
	if _, err := adapter.Acquire(context.Background(), "holder", 30*time.Second); err == nil {
		t.Fatal("missing precreated Lease accepted")
	}
	if api.updates != 0 {
		t.Fatal("created or updated missing Lease")
	}
}
