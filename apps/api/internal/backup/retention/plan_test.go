package retention

import (
	"errors"
	"reflect"
	"testing"
	"time"
)

var testNow = time.Date(2026, 9, 26, 12, 0, 0, 0, time.UTC)

func days(n int) time.Time { return testNow.Add(time.Duration(n) * 24 * time.Hour) }

func fixture() (Catalog, VersionInventory, []VerifiedRecovery) {
	chains := []Chain{
		{ID: "old", Full: Full{ID: "old-full", At: days(-90), ObjectIDs: []string{"old-f"}}, Incrementals: []Incremental{{ID: "old-inc", From: days(-90), Through: days(-60), ObjectIDs: []string{"old-i"}}}},
		{ID: "crossing", Full: Full{ID: "cross-full", At: days(-40), ObjectIDs: []string{"cross-f"}}, Incrementals: []Incremental{{ID: "cross-inc", From: days(-40), Through: days(-20), ObjectIDs: []string{"cross-i"}}}},
		{ID: "new", Full: Full{ID: "new-full", At: days(-20), ObjectIDs: []string{"new-f"}}, Incrementals: []Incremental{{ID: "new-inc", From: days(-20), Through: testNow, ObjectIDs: []string{"new-i"}}}},
	}
	versions := []ObjectVersion{
		{ObjectID: "old-f", VersionID: "v1", SizeBytes: 10, Current: true},
		{ObjectID: "old-i", VersionID: "v2", SizeBytes: 20, Current: true},
		{ObjectID: "cross-f", VersionID: "v3", SizeBytes: 30, Current: true},
		{ObjectID: "cross-i", VersionID: "v4", SizeBytes: 40, Current: true},
		{ObjectID: "new-f", VersionID: "v5", SizeBytes: 50, Current: true},
		{ObjectID: "new-i", VersionID: "v6", SizeBytes: 60, Current: true},
	}
	return Catalog{Pages: []CatalogPage{{Chains: chains, Complete: true}}},
		VersionInventory{Pages: []VersionPage{{Versions: versions, Complete: true}}},
		[]VerifiedRecovery{{ChainID: "new", FileChecked: true, CheckedAt: testNow}}
}

func refusalCode(t *testing.T, err error) RefusalCode {
	t.Helper()
	var refusal *Refusal
	if !errors.As(err, &refusal) {
		t.Fatalf("want typed refusal, got %v", err)
	}
	return refusal.Code
}

func TestPlanRetiresOnlyWholeOldChain(t *testing.T) {
	catalog, inventory, verified := fixture()
	got, err := Plan(catalog, inventory, verified, days(-35), testNow)
	if err != nil {
		t.Fatal(err)
	}
	want := []TargetVersion{{ObjectID: "old-f", VersionID: "v1", SizeBytes: 10}, {ObjectID: "old-i", VersionID: "v2", SizeBytes: 20}}
	if !reflect.DeepEqual(got.Targets, want) {
		t.Fatalf("targets = %#v, want %#v", got.Targets, want)
	}
	if !reflect.DeepEqual(got.RetiredChains, []string{"old"}) || !reflect.DeepEqual(got.ProtectedChains, []string{"crossing", "new"}) {
		t.Fatalf("chain decisions = retired %v, protected %v", got.RetiredChains, got.ProtectedChains)
	}
	if got.ExpectedFreedBytes != 30 || got.StorageOverhangBytes != 70 || got.TargetRange.From != days(-90) || got.TargetRange.Through != days(-60) {
		t.Fatalf("target metadata = %#v", got)
	}
	if got.Coverage.From != days(-40) || got.Coverage.Through != testNow || got.ExpiresAt != testNow.Add(15*time.Minute) || got.CatalogDigest == "" || got.ApprovalHash == "" {
		t.Fatalf("coverage/approval metadata = %#v", got)
	}
}

func TestPlanProtectsCutoffCrossingChain(t *testing.T) {
	catalog, inventory, verified := fixture()
	catalog.Pages[0].Chains = catalog.Pages[0].Chains[1:]
	inventory.Pages[0].Versions = inventory.Pages[0].Versions[2:]
	got, err := Plan(catalog, inventory, verified, days(-35), testNow)
	if err != nil || len(got.Targets) != 0 {
		t.Fatalf("cutoff-crossing chain must remain protected: %#v, %v", got, err)
	}
	if !reflect.DeepEqual(got.ProtectedChains, []string{"crossing", "new"}) {
		t.Fatalf("protected = %v", got.ProtectedChains)
	}
}

func TestPlanRefusesUnsafeInput(t *testing.T) {
	checks := []struct {
		name   string
		change func(*Catalog, *VersionInventory, *[]VerifiedRecovery)
		code   RefusalCode
	}{
		{"catalog next page missing", func(c *Catalog, _ *VersionInventory, _ *[]VerifiedRecovery) {
			c.Pages[0].Complete = false
			c.Pages[0].NextCursor = "page-2"
		}, RefusalIncomplete},
		{"object next page missing", func(_ *Catalog, v *VersionInventory, _ *[]VerifiedRecovery) {
			v.Pages[0].Complete = false
			v.Pages[0].NextCursor = "page-2"
		}, RefusalIncomplete},
		{"missing object", func(_ *Catalog, v *VersionInventory, _ *[]VerifiedRecovery) {
			v.Pages[0].Versions = v.Pages[0].Versions[1:]
		}, RefusalIncomplete},
		{"unexpected object", func(_ *Catalog, v *VersionInventory, _ *[]VerifiedRecovery) {
			v.Pages[0].Versions = append(v.Pages[0].Versions, ObjectVersion{ObjectID: "orphan", VersionID: "v7", Current: true})
		}, RefusalAmbiguous},
		{"duplicate version", func(_ *Catalog, v *VersionInventory, _ *[]VerifiedRecovery) {
			v.Pages[0].Versions = append(v.Pages[0].Versions, v.Pages[0].Versions[0])
		}, RefusalAmbiguous},
		{"noncurrent version", func(_ *Catalog, v *VersionInventory, _ *[]VerifiedRecovery) { v.Pages[0].Versions[0].Current = false }, RefusalAmbiguous},
		{"active backup", func(c *Catalog, _ *VersionInventory, _ *[]VerifiedRecovery) { c.ActiveBackup = true }, RefusalConcurrent},
		{"active restore", func(c *Catalog, _ *VersionInventory, _ *[]VerifiedRecovery) { c.ActiveRestore = true }, RefusalConcurrent},
		{"locked target", func(_ *Catalog, v *VersionInventory, _ *[]VerifiedRecovery) {
			v.Pages[0].Versions[0].LockUntil = testNow.Add(time.Hour)
		}, RefusalLocked},
		{"new full not file checked", func(_ *Catalog, _ *VersionInventory, r *[]VerifiedRecovery) { (*r)[0].FileChecked = false }, RefusalUnverified},
		{"new full check stale", func(_ *Catalog, _ *VersionInventory, r *[]VerifiedRecovery) { (*r)[0].CheckedAt = days(-41) }, RefusalUnverified},
		{"coverage gap", func(c *Catalog, _ *VersionInventory, _ *[]VerifiedRecovery) {
			c.Pages[0].Chains[2].Full.At = days(-19)
			c.Pages[0].Chains[2].Incrementals[0].From = days(-19)
		}, RefusalCoverage},
		{"no cutoff coverage", func(c *Catalog, _ *VersionInventory, _ *[]VerifiedRecovery) {
			c.Pages[0].Chains[1].Full.At = days(-34)
			c.Pages[0].Chains[1].Incrementals[0].From = days(-34)
		}, RefusalCoverage},
		{"incomplete incremental", func(c *Catalog, _ *VersionInventory, _ *[]VerifiedRecovery) {
			c.Pages[0].Chains[1].Incrementals[0].From = days(-39)
		}, RefusalAmbiguous},
		{"unhashable lock time", func(_ *Catalog, v *VersionInventory, _ *[]VerifiedRecovery) {
			v.Pages[0].Versions[4].LockUntil = time.Date(10000, 1, 1, 0, 0, 0, 0, time.UTC)
		}, RefusalAmbiguous},
	}
	for _, check := range checks {
		t.Run(check.name, func(t *testing.T) {
			catalog, inventory, verified := fixture()
			check.change(&catalog, &inventory, &verified)
			got, err := Plan(catalog, inventory, verified, days(-35), testNow)
			if code := refusalCode(t, err); code != check.code || len(got.Targets) != 0 {
				t.Fatalf("code = %s, plan = %#v; want %s", code, got, check.code)
			}
		})
	}
}

func TestPlanRequiresVerifiedNewerFull(t *testing.T) {
	catalog, inventory, verified := fixture()
	catalog.Pages[0].Chains = catalog.Pages[0].Chains[:2]
	inventory.Pages[0].Versions = inventory.Pages[0].Versions[:4]
	catalog.Pages[0].Chains[1].Incrementals[0].Through = testNow
	verified = nil
	if code := refusalCode(t, func() error { _, err := Plan(catalog, inventory, verified, days(-35), testNow); return err }()); code != RefusalUnverified {
		t.Fatalf("code = %s", code)
	}
}

func TestPlanCanonicalHashAcrossPaginationAndOrder(t *testing.T) {
	catalog, inventory, verified := fixture()
	first, err := Plan(catalog, inventory, verified, days(-35), testNow)
	if err != nil {
		t.Fatal(err)
	}
	catalog.Pages = []CatalogPage{
		{Chains: []Chain{catalog.Pages[0].Chains[2]}, NextCursor: "second"},
		{Cursor: "second", Chains: []Chain{catalog.Pages[0].Chains[1], catalog.Pages[0].Chains[0]}, Complete: true},
	}
	versions := inventory.Pages[0].Versions
	inventory.Pages = []VersionPage{
		{Versions: []ObjectVersion{versions[5], versions[4], versions[3]}, NextCursor: "second"},
		{Cursor: "second", Versions: []ObjectVersion{versions[2], versions[1], versions[0]}, Complete: true},
	}
	second, err := Plan(catalog, inventory, verified, days(-35), testNow)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(first, second) {
		t.Fatalf("pagination/order changed plan:\n%#v\n%#v", first, second)
	}
	inventory.Pages[1].Versions[2].VersionID = "replaced"
	changed, err := Plan(catalog, inventory, verified, days(-35), testNow)
	if err != nil {
		t.Fatal(err)
	}
	if first.CatalogDigest == changed.CatalogDigest || first.ApprovalHash == changed.ApprovalHash {
		t.Fatalf("changed target version did not change digests")
	}
}

func FuzzPlanVersionOrderDoesNotChangeApproval(f *testing.F) {
	f.Add([]byte{0, 1, 2, 3, 4, 5})
	f.Add([]byte{255, 17, 5})
	f.Fuzz(func(t *testing.T, order []byte) {
		if len(order) == 0 {
			return
		}
		catalog, inventory, verified := fixture()
		base, err := Plan(catalog, inventory, verified, days(-35), testNow)
		if err != nil {
			t.Fatal(err)
		}
		versions := inventory.Pages[0].Versions
		for i, value := range order {
			j := int(value) % len(versions)
			versions[i%len(versions)], versions[j] = versions[j], versions[i%len(versions)]
		}
		got, err := Plan(catalog, inventory, verified, days(-35), testNow)
		if err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(base, got) {
			t.Fatalf("version order changed plan")
		}
	})
}
