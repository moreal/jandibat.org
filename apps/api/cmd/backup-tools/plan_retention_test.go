package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"
)

func TestPlanRetentionReportsOnlyCatalogDeclaredCoverage(t *testing.T) {
	now := time.Date(2026, 9, 27, 12, 0, 0, 0, time.UTC)
	input := `{"schemaVersion":1,"catalog":{"Namespace":{"Bucket":"private-bucket","Prefix":"secret-prefix/"},"Pages":[{"Complete":true,"Chains":[{"ID":"secret-chain","Full":{"ID":"secret-full","At":"2026-08-20T12:00:00Z","ObjectIDs":["secret-object"]},"Incrementals":[{"ID":"secret-incremental","From":"2026-08-20T12:00:00Z","Through":"2026-09-27T11:30:00Z","ObjectIDs":["secret-object-2"]}]}]}]}}`
	var out bytes.Buffer
	if err := runPlanRetention(strings.NewReader(input), &out, now); err != nil {
		t.Fatal(err)
	}
	var got map[string]json.RawMessage
	if err := json.Unmarshal(out.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if len(got) != 5 || string(got["schemaVersion"]) != "1" || string(got["mode"]) != `"diagnostic"` ||
		string(got["storageOverhangBytes"]) != "null" || string(got["deletionTargets"]) != "[]" ||
		string(got["catalogDeclaredProtectedInterval"]) != `{"from":"2026-08-20T12:00:00Z","through":"2026-09-27T11:30:00Z"}` {
		t.Fatalf("unexpected diagnostic fields: %s", out.String())
	}
	for _, private := range []string{"private-bucket", "secret-prefix", "secret-chain", "secret-object", "approvalHash", "versionId", "http"} {
		if strings.Contains(out.String(), private) {
			t.Fatalf("private or actionable data exposed: %s", out.String())
		}
	}
}

func TestPlanRetentionModeDoesNotReadVerifierConfiguration(t *testing.T) {
	now := time.Now().UTC()
	fullAt := now.Add(-40 * 24 * time.Hour).Format(time.RFC3339Nano)
	through := now.Add(-30 * time.Minute).Format(time.RFC3339Nano)
	input := fmt.Sprintf(`{"schemaVersion":1,"catalog":{"Namespace":{"Bucket":"bucket","Prefix":"prefix/"},"Pages":[{"Complete":true,"Chains":[{"ID":"chain","Full":{"ID":"full","At":%q,"ObjectIDs":["obj"]},"Incrementals":[{"ID":"inc","From":%q,"Through":%q,"ObjectIDs":["obj2"]}]}]}]}}`, fullAt, fullAt, through)
	file, err := os.CreateTemp(t.TempDir(), "catalog-")
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	if _, err := file.WriteString(input); err != nil {
		t.Fatal(err)
	}
	if _, err := file.Seek(0, 0); err != nil {
		t.Fatal(err)
	}
	previous := os.Stdin
	os.Stdin = file
	defer func() { os.Stdin = previous }()
	var out bytes.Buffer
	err = runMode(context.Background(), []string{"plan-retention"}, func(string) string {
		t.Fatal("retention diagnostic accessed verifier configuration")
		return ""
	}, nil, nil, nil, &out)
	if err != nil || !bytes.Contains(out.Bytes(), []byte(`"mode":"diagnostic"`)) {
		t.Fatalf("diagnostic unavailable without verifier configuration: %v %q", err, out.String())
	}
}

func TestPlanRetentionRejectsUnsafeOrIncompleteInputWithoutEcho(t *testing.T) {
	now := time.Date(2026, 9, 27, 12, 0, 0, 0, time.UTC)
	good := `{"schemaVersion":1,"catalog":{"Namespace":{"Bucket":"bucket","Prefix":"prefix/"},"Pages":[{"Complete":true,"Chains":[{"ID":"chain","Full":{"ID":"full","At":"2026-08-20T12:00:00Z","ObjectIDs":["obj"]},"Incrementals":[{"ID":"inc","From":"2026-08-20T12:00:00Z","Through":"2026-09-27T11:30:00Z","ObjectIDs":["obj2"]}]}]}]}}`
	for name, input := range map[string]string{
		"missing catalog":           `{"schemaVersion":1}`,
		"wrong version":             strings.Replace(good, `"schemaVersion":1`, `"schemaVersion":2`, 1),
		"mapping input":             good[:len(good)-1] + `,"versionedObjects":[{"key":"secret-key","versionId":"secret-version"}]}`,
		"approval input":            good[:len(good)-1] + `,"approvalHash":"secret-hash"}`,
		"duplicate nested field":    strings.Replace(good, `"Bucket":"bucket"`, `"Bucket":"bucket","Bucket":"other"`, 1),
		"case alias active backup":  strings.Replace(good, `"Namespace":`, `"ActiveBackup":true,"activebackup":false,"Namespace":`, 1),
		"case alias completed page": strings.Replace(good, `"Complete":true`, `"Complete":false,"complete":true`, 1),
		"case alias chain identity": strings.Replace(good, `"ID":"chain"`, `"ID":"secret-chain","id":"chain"`, 1),
		"second document":           good + good,
		"incomplete page":           strings.Replace(good, `"Complete":true`, `"Complete":false`, 1),
		"coverage gap":              strings.Replace(good, `"At":"2026-08-20T12:00:00Z"`, `"At":"2026-09-01T12:00:00Z"`, 1),
		"oversized":                 good + strings.Repeat(" ", maxPlanRetentionInput),
	} {
		t.Run(name, func(t *testing.T) {
			var out bytes.Buffer
			err := runPlanRetention(strings.NewReader(input), &out, now)
			if err == nil || strings.Contains(err.Error(), "secret") || out.Len() != 0 {
				t.Fatalf("unsafe acceptance or echo: error=%v output=%q", err, out.String())
			}
		})
	}
}
