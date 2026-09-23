package cockroach

import (
	"errors"
	"testing"

	"github.com/moreal/jandibat.org/apps/api/internal/domain/activity"
	"github.com/moreal/jandibat.org/apps/api/internal/integrations"
)

func TestSaveConnectionActivityRejectsInvalidFenceBeforeDatabase(t *testing.T) {
	store := &Store{}
	for _, id := range []string{"", "not-a-uuid"} {
		err := store.SaveConnectionActivity(t.Context(), id, "claim", activity.SaveEnvironmentsInput{}, activity.SaveFactsInput{})
		if !errors.Is(err, integrations.ErrInvalidConnectionStatus) {
			t.Fatalf("connection %q error = %v", id, err)
		}
	}
}
