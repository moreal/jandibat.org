package integrations

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestConnectionPageKeysetTraversalAndVisibility(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store := NewMemoryStore()
	service := &ConnectionService{store: store}
	base := time.Date(2026, 9, 24, 0, 0, 0, 0, time.UTC)
	ids := []string{
		"11111111-1111-4111-8111-111111111111",
		"22222222-2222-4222-8222-222222222222",
		"33333333-3333-4333-8333-333333333333",
		"44444444-4444-4444-8444-444444444444",
	}
	for i, id := range ids {
		created := base
		if i == 3 {
			created = base.Add(time.Second)
		}
		if err := store.SaveConnection(ctx, ConnectionRecord{
			Connection:  ProviderConnection{ID: id, SubjectID: "owner", ProviderID: "github", EnvironmentID: "connection:" + id, Status: ConnectionActive, CreatedAt: created},
			Credentials: EncryptedCredentials{AccessToken: []byte("secret")},
		}); err != nil {
			t.Fatal(err)
		}
	}
	for _, record := range []ConnectionRecord{
		{Connection: ProviderConnection{ID: "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa", SubjectID: "other", ProviderID: "github", EnvironmentID: "connection:other", Status: ConnectionActive, CreatedAt: base}},
		{Connection: ProviderConnection{ID: "bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb", SubjectID: "owner", ProviderID: "github", EnvironmentID: "connection:revoked", Status: ConnectionRevoked, CreatedAt: base}},
	} {
		if err := store.SaveConnection(ctx, record); err != nil {
			t.Fatal(err)
		}
	}
	first, err := service.ListPage(ctx, "owner", nil, 2)
	if err != nil || len(first.Connections) != 2 || first.Connections[0].ID != ids[0] || first.Connections[1].ID != ids[1] || !first.HasNextPage {
		t.Fatalf("first page = (%+v, %v)", first, err)
	}
	store.mu.Lock()
	delete(store.connections, ids[1])
	store.mu.Unlock()
	second, err := service.ListPage(ctx, "owner", &ConnectionCursor{CreatedAt: base, ID: ids[1]}, 2)
	if err != nil || len(second.Connections) != 2 || second.Connections[0].ID != ids[2] || second.Connections[1].ID != ids[3] || second.HasNextPage {
		t.Fatalf("second page after deleted anchor = (%+v, %v)", second, err)
	}
	end, err := service.ListPage(ctx, "owner", &ConnectionCursor{CreatedAt: base.Add(time.Second), ID: ids[3]}, 2)
	if err != nil || len(end.Connections) != 0 || end.HasNextPage {
		t.Fatalf("terminal page = (%+v, %v)", end, err)
	}
}

func TestCustomProviderPageKeysetTraversalWithArbitrarySlugs(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store := NewMemoryStore()
	service := &CustomProviderService{store: store}
	base := time.Date(2026, 9, 24, 0, 0, 0, 0, time.UTC)
	ids := []string{
		"11111111-1111-4111-8111-111111111111",
		"22222222-2222-4222-8222-222222222222",
		"33333333-3333-4333-8333-333333333333",
	}
	for i, id := range ids {
		if err := store.SaveCustomProvider(ctx, CustomProviderRecord{
			Provider:              CustomProvider{ID: id, SubjectID: "owner", Slug: []string{"zeta", "alpha", "middle"}[i], CreatedAt: base},
			EncryptedIngestSecret: []byte("secret-hash"),
		}); err != nil {
			t.Fatal(err)
		}
	}
	if err := store.SaveCustomProvider(ctx, CustomProviderRecord{Provider: CustomProvider{ID: "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa", SubjectID: "other", Slug: "first", CreatedAt: base}}); err != nil {
		t.Fatal(err)
	}
	first, err := service.ListPage(ctx, "owner", nil, 2)
	if err != nil || len(first.Providers) != 2 || first.Providers[0].ID != ids[0] || first.Providers[1].ID != ids[1] || !first.HasNextPage {
		t.Fatalf("first page = (%+v, %v)", first, err)
	}
	store.mu.Lock()
	delete(store.customProviders, ids[1])
	store.mu.Unlock()
	second, err := service.ListPage(ctx, "owner", &CustomProviderCursor{CreatedAt: base, ID: ids[1]}, 2)
	if err != nil || len(second.Providers) != 1 || second.Providers[0].ID != ids[2] || second.HasNextPage {
		t.Fatalf("second page after deleted anchor = (%+v, %v)", second, err)
	}
}

func TestIntegrationPagesRejectInvalidBoundsAndCursor(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	connections := &ConnectionService{store: NewMemoryStore()}
	providers := &CustomProviderService{store: NewMemoryStore()}
	for _, first := range []int{-1, 0, 101} {
		if _, err := connections.ListPage(ctx, "owner", nil, first); !errors.Is(err, ErrInvalidIntegrationPageSize) {
			t.Errorf("connection first=%d error=%v", first, err)
		}
		if _, err := providers.ListPage(ctx, "owner", nil, first); !errors.Is(err, ErrInvalidIntegrationPageSize) {
			t.Errorf("provider first=%d error=%v", first, err)
		}
	}
	if _, err := connections.ListPage(ctx, "", nil, 1); !errors.Is(err, ErrEmptySubjectID) {
		t.Errorf("connection empty subject error=%v", err)
	}
	if _, err := providers.ListPage(ctx, "", nil, 1); !errors.Is(err, ErrEmptySubjectID) {
		t.Errorf("provider empty subject error=%v", err)
	}
	for _, after := range []ConnectionCursor{
		{ID: "11111111-1111-4111-8111-111111111111"},
		{CreatedAt: time.Now(), ID: "bad"},
		{CreatedAt: time.Now(), ID: "11111111-1111-4111-8111-AAAAAAAAAAAA"},
	} {
		if _, err := connections.ListPage(ctx, "owner", &after, 1); !errors.Is(err, ErrInvalidIdentifier) {
			t.Errorf("connection cursor %+v error=%v", after, err)
		}
		providerAfter := CustomProviderCursor(after)
		if _, err := providers.ListPage(ctx, "owner", &providerAfter, 1); !errors.Is(err, ErrInvalidIdentifier) {
			t.Errorf("provider cursor %+v error=%v", after, err)
		}
	}
}
