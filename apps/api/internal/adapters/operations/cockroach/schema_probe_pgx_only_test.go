package cockroach

import (
	"context"
	"errors"
	"strings"
	"testing"
)

// Readiness requires a pgx pool; an unconfigured store fails closed.
func TestOperationsSchemaProbeRequiresPGXPool(t *testing.T) {
	store := &Store{}
	if err := store.Check(context.Background()); !errors.Is(err, ErrNilDB) {
		t.Fatalf("Check without a pgx pool = %v, want ErrNilDB", err)
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
