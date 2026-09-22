package staticanalysis_test

import (
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestExhaustivenessAnalyzersReportIncompleteFixtures(t *testing.T) {
	t.Parallel()

	moduleRoot := filepath.Join("..", "..")
	fixture := "internal/staticanalysis/testdata/src/exhaustiveness/exhaustiveness.go"

	t.Run("exhaustive", func(t *testing.T) {
		assertAnalyzerReports(t, moduleRoot, "exhaustive", []string{"-check=switch,map", fixture}, "policyStateDone")
	})

	t.Run("go-check-sumtype", func(t *testing.T) {
		assertAnalyzerReports(t, moduleRoot, "go-check-sumtype", []string{fixture}, "deletedEvent")
	})
}

func assertAnalyzerReports(t *testing.T, workingDirectory, analyzer string, arguments []string, missingCase string) {
	t.Helper()

	command := exec.Command(analyzer, arguments...)
	command.Dir = workingDirectory
	output, err := command.CombinedOutput()
	if err == nil {
		t.Fatalf("%s unexpectedly accepted an intentionally incomplete fixture; output:\n%s", analyzer, output)
	}
	if !strings.Contains(string(output), missingCase) {
		t.Fatalf("%s did not identify missing case %q; output:\n%s", analyzer, missingCase, output)
	}
}
