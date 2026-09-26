package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"time"

	"github.com/moreal/jandibat.org/apps/api/internal/backup/retention"
)

const maxPlanRetentionInput = 1 << 20

// This mode reports catalog chronology only. It has no path from stdin to a
// version inventory, file-check evidence, or an actionable retention plan.
func runPlanRetention(input io.Reader, output io.Writer, now time.Time) error {
	if input == nil || output == nil || now.IsZero() {
		return errors.New("invalid retention diagnostic")
	}
	data, err := io.ReadAll(io.LimitReader(input, maxPlanRetentionInput+1))
	if err != nil || len(data) == 0 || len(data) > maxPlanRetentionInput || !uniqueJSONKeys(data) {
		return errors.New("invalid retention diagnostic input")
	}
	var request struct {
		SchemaVersion int               `json:"schemaVersion"`
		Catalog       retention.Catalog `json:"catalog"`
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&request) != nil || request.SchemaVersion != 1 {
		return errors.New("invalid retention diagnostic input")
	}
	if err := decoder.Decode(new(any)); !errors.Is(err, io.EOF) {
		return errors.New("invalid retention diagnostic input")
	}
	_, planErr := retention.Plan(request.Catalog, retention.VersionInventory{}, nil, now.Add(-35*24*time.Hour), now)
	var refusal *retention.Refusal
	if !errors.As(planErr, &refusal) || refusal.Report == nil || refusal.Report.Coverage.From.IsZero() || refusal.Report.Coverage.Through.IsZero() {
		return errors.New("catalog chronology unavailable")
	}
	response := struct {
		SchemaVersion                    int    `json:"schemaVersion"`
		Mode                             string `json:"mode"`
		CatalogDeclaredProtectedInterval struct {
			From    time.Time `json:"from"`
			Through time.Time `json:"through"`
		} `json:"catalogDeclaredProtectedInterval"`
		StorageOverhangBytes *int64   `json:"storageOverhangBytes"`
		DeletionTargets      []string `json:"deletionTargets"`
	}{SchemaVersion: 1, Mode: "diagnostic", DeletionTargets: []string{}}
	response.CatalogDeclaredProtectedInterval.From = refusal.Report.Coverage.From
	response.CatalogDeclaredProtectedInterval.Through = refusal.Report.Coverage.Through
	encoded, err := json.Marshal(response)
	if err != nil {
		return errors.New("retention diagnostic unavailable")
	}
	encoded = append(encoded, '\n')
	_, err = output.Write(encoded)
	if err != nil {
		return errors.New("retention diagnostic output failed")
	}
	return nil
}

func uniqueJSONKeys(data []byte) bool {
	decoder := json.NewDecoder(bytes.NewReader(data))
	if !scanUniqueJSONValue(decoder, 0) {
		return false
	}
	_, err := decoder.Token()
	return errors.Is(err, io.EOF)
}

func scanUniqueJSONValue(decoder *json.Decoder, depth int) bool {
	if depth > 16 {
		return false
	}
	token, err := decoder.Token()
	if err != nil {
		return false
	}
	delim, ok := token.(json.Delim)
	if !ok {
		return true
	}
	switch delim {
	case '{':
		seen := map[string]bool{}
		for decoder.More() {
			keyToken, err := decoder.Token()
			key, ok := keyToken.(string)
			canonicalKey := strings.ToLower(key)
			if err != nil || !ok || seen[canonicalKey] || !scanUniqueJSONValue(decoder, depth+1) {
				return false
			}
			seen[canonicalKey] = true
		}
	case '[':
		for decoder.More() {
			if !scanUniqueJSONValue(decoder, depth+1) {
				return false
			}
		}
	default:
		return false
	}
	end, err := decoder.Token()
	return err == nil && end == matchingJSONDelimiter(delim)
}

func matchingJSONDelimiter(open json.Delim) json.Delim {
	if open == '{' {
		return '}'
	}
	return ']'
}
