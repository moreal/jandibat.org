package cockroach

import (
	"context"
	"errors"
	"fmt"
	"math"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
	appdb "github.com/moreal/jandibat.org/apps/api/internal/database"
	"github.com/moreal/jandibat.org/apps/api/internal/subjects"
)

func TestNewRejectsNilPool(t *testing.T) {
	store, err := New(nil)
	if !errors.Is(err, ErrNilDB) || store != nil {
		t.Fatalf("New(nil) = (%v, %v), want (nil, ErrNilDB)", store, err)
	}
}

func TestPersistenceErrorMapsConstraintFailures(t *testing.T) {
	tests := []struct {
		code   string
		target error
	}{
		{"23505", subjects.ErrConflict}, {"23503", subjects.ErrNotFound}, {"23514", subjects.ErrInvalidInput},
	}
	for _, test := range tests {
		t.Run(test.code, func(t *testing.T) {
			got := persistenceError(&pgconn.PgError{Code: test.code})
			if !errors.Is(got, test.target) {
				t.Fatalf("persistenceError() = %v, want %v", got, test.target)
			}
		})
	}
}

func TestClaimOrCreateSubjectRejectsMismatchedSettingsBeforeDatabase(t *testing.T) {
	store := &Store{}
	_, err := store.ClaimOrCreateSubject(t.Context(), subjects.Subject{ID: "subject", OwnerUserID: "user", Handle: "handle"}, subjects.SubjectSettings{SubjectID: "different"})
	if !errors.Is(err, subjects.ErrInvalidInput) {
		t.Fatalf("ClaimOrCreateSubject() = %v, want invalid input", err)
	}
}

func TestSubjectSettingsRepositoriesRejectOutOfRangeSyncIntervalsBeforeDatabase(t *testing.T) {
	store := &Store{}
	now := time.Now()
	subject := subjects.Subject{ID: "subject", OwnerUserID: "owner", Handle: "handle", Timezone: "UTC", CreatedAt: now, UpdatedAt: now}
	settings := subjects.SubjectSettings{SubjectID: subject.ID, Timezone: subject.Timezone, SyncIntervalMinutes: 60, UpdatedAt: now}
	for _, interval := range []int{14, 10081, math.MaxInt} {
		t.Run(fmt.Sprintf("interval_%d", interval), func(t *testing.T) {
			settings.SyncIntervalMinutes = interval
			t.Run("create", func(t *testing.T) {
				if _, err := store.ClaimOrCreateSubject(context.Background(), subject, settings); !errors.Is(err, subjects.ErrInvalidInput) {
					t.Fatalf("ClaimOrCreateSubject() error = %v, want invalid input", err)
				}
			})
			t.Run("save", func(t *testing.T) {
				if err := store.SaveSubjectSettings(context.Background(), settings); !errors.Is(err, subjects.ErrInvalidInput) {
					t.Fatalf("SaveSubjectSettings() error = %v, want invalid input", err)
				}
			})
		})
	}
}

func TestSubjectSettingsRepositoriesAcceptSyncIntervalEndpoints(t *testing.T) {
	store := &Store{}
	now := time.Now()
	subject := subjects.Subject{ID: "subject", OwnerUserID: "owner", Handle: "handle", Timezone: "UTC", CreatedAt: now, UpdatedAt: now}
	settings := subjects.SubjectSettings{SubjectID: subject.ID, Timezone: subject.Timezone, UpdatedAt: now}
	for _, interval := range []int{15, 10080} {
		settings.SyncIntervalMinutes = interval
		if _, err := store.ClaimOrCreateSubject(context.Background(), subject, settings); !errors.Is(err, appdb.ErrInvalidPool) {
			t.Fatalf("ClaimOrCreateSubject(%d) error = %v, want nil pool", interval, err)
		}
		if err := store.SaveSubjectSettings(context.Background(), settings); !errors.Is(err, appdb.ErrInvalidPool) {
			t.Fatalf("SaveSubjectSettings(%d) error = %v, want nil pool", interval, err)
		}
	}
}
