package cockroach

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/moreal/jandibat.org/apps/api/internal/adapters/internal/fakedb"
)

// Readiness must not silently probe through a legacy SQL handle. A closed
// handle makes the wrong path observable without depending on query text.
func TestOperationsSchemaProbeRequiresPGXPool(t *testing.T) {
	db := fakedb.New().Open()
	store, err := New(db)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	if err := store.Check(context.Background()); !errors.Is(err, ErrNilDB) {
		t.Fatalf("Check with only a closed SQL handle = %v, want ErrNilDB", err)
	}
}

func TestOperationsSchemaProbeRequiresAll104Columns(t *testing.T) {
	if err := validateOperationsSchemaColumnCount(104); err != nil {
		t.Fatalf("104 required columns: %v", err)
	}
	if err := validateOperationsSchemaColumnCount(103); err == nil || !strings.Contains(err.Error(), "found 103 of 104 required columns") {
		t.Fatalf("103 required columns: %v, want incomplete-schema error", err)
	}
}
