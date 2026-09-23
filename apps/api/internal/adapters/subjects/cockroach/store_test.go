package cockroach

import (
	"errors"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"
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
