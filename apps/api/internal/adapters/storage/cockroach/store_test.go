package cockroach

import (
	"errors"
	"testing"

	"github.com/moreal/jandibat.org/apps/api/internal/domain/activity"
)

func TestNewRejectsNilPool(t *testing.T) {
	store, err := New(nil)
	if !errors.Is(err, ErrNilDB) || store != nil {
		t.Fatalf("New(nil) = (%v, %v), want ErrNilDB", store, err)
	}
}

func TestDecodeMetadataRejectsNonStringValues(t *testing.T) {
	if _, err := decodeMetadata([]byte(`{"count": 1}`)); err == nil {
		t.Fatal("decodeMetadata accepted non-string value")
	}
}

func TestReplaceFactsRejectsInvalidScopeBeforeDatabase(t *testing.T) {
	store := &Store{}
	from, to := activity.Date("2026-02-01"), activity.Date("2026-02-28")
	filter := activity.LoadFactsInput{Subject: "alice", From: &from, To: &to}
	outside := activity.Fact{Subject: "alice", Date: "2026-03-01", EnvironmentID: "github"}
	if err := store.ReplaceFacts(t.Context(), filter, nil, []activity.Fact{outside}); !errors.Is(err, ErrFactOutsideRange) {
		t.Fatalf("outside date = %v", err)
	}
	inside := activity.Fact{Subject: "alice", Date: "2026-02-12", EnvironmentID: "github"}
	if err := store.ReplaceFacts(t.Context(), filter, []activity.EnvironmentID{"gitlab"}, []activity.Fact{inside}); !errors.Is(err, ErrEnvironmentOutside) {
		t.Fatalf("outside environment = %v", err)
	}
	inside.Subject = "other"
	if err := store.ReplaceFacts(t.Context(), filter, nil, []activity.Fact{inside}); !errors.Is(err, activity.ErrMismatchedFactSubject) {
		t.Fatalf("mismatched subject = %v", err)
	}
}

func TestFactDateBoundsAndRange(t *testing.T) {
	from, to := activity.Date("2026-02-01"), activity.Date("2026-02-28")
	filter := activity.LoadFactsInput{Subject: "alice", From: &from, To: &to}
	left, right, err := factDateBounds(filter)
	if err != nil || left.Format("2006-01-02") != string(from) || right.Format("2006-01-02") != string(to) {
		t.Fatalf("factDateBounds = (%v, %v, %v)", left, right, err)
	}
	if !dateInRange("2026-02-12", &from, &to) || dateInRange("2026-03-01", &from, &to) {
		t.Fatal("dateInRange lost inclusive range semantics")
	}
}
