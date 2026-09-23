package cockroach

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/moreal/jandibat.org/apps/api/internal/integrations"
)

func TestConnectionOperationsRequirePGXPool(t *testing.T) {
	store := &Store{}
	now := time.Date(2026, 8, 12, 12, 0, 0, 0, time.UTC)
	id := "018f0000-0000-7000-8000-000000000001"
	record := integrations.ConnectionRecord{Connection: integrations.ProviderConnection{
		ID: id, SubjectID: "subject-1", ProviderID: "gitlab",
		EnvironmentID: "environment-1", AuthMethod: integrations.AuthToken,
		Status: integrations.ConnectionActive, CreatedAt: now, UpdatedAt: now,
	}}
	check := func(name string, got error) {
		t.Helper()
		if !errors.Is(got, ErrNilDB) {
			t.Errorf("%s without pgx pool error = %v, want %v", name, got, ErrNilDB)
		}
	}
	check("SaveConnection", store.SaveConnection(context.Background(), record))
	_, err := store.GetConnection(context.Background(), id)
	check("GetConnection", err)
	_, err = store.ListConnections(context.Background(), "subject-1")
	check("ListConnections", err)
	_, err = store.ListConnectionsPage(context.Background(), "subject-1", nil, 1)
	check("ListConnectionsPage", err)
	check("UpdateConnectionAfterSync", store.UpdateConnectionAfterSync(context.Background(), record, id))
	check("PurgeConnectionData", store.PurgeConnectionData(context.Background(), id))
}
