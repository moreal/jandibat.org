package graphql

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/99designs/gqlgen/codegen/config"
	"github.com/goccy/go-yaml"
)

func TestConfiguredSchemaGlobIncludesRootAndNestedSDL(t *testing.T) {
	contents, err := os.ReadFile("../../gqlgen.yml")
	if err != nil {
		t.Fatal(err)
	}
	var actual struct {
		Schema []string `yaml:"schema"`
	}
	if err := yaml.Unmarshal(contents, &actual); err != nil {
		t.Fatal(err)
	}
	if len(actual.Schema) != 1 {
		t.Fatalf("expected one authoritative SDL glob, got %d", len(actual.Schema))
	}
	pattern := strings.TrimPrefix(actual.Schema[0], "../../graphql/schema/")
	if pattern == actual.Schema[0] {
		t.Fatal("configured schema root is not graphql/schema")
	}

	schemaDir := t.TempDir()
	if err := os.Mkdir(filepath.Join(schemaDir, "nested"), 0o755); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"root.graphqls", "nested/child.graphqls"} {
		if err := os.WriteFile(filepath.Join(schemaDir, name), []byte("scalar Example"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	cfg, err := config.ReadConfig(strings.NewReader(fmt.Sprintf("schema:\n  - %q\n", filepath.Join(schemaDir, pattern))))
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.Sources) != 2 {
		t.Fatalf("configured SDL glob loaded %d sources, want root and nested", len(cfg.Sources))
	}
}
