package activity

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"time"

	domain "github.com/moreal/jandibat.org/apps/api/internal/domain/activity"
)

// AudienceScope identifies the authorization decision that produced the
// timeline. Different audiences must not accidentally share a cache revision.
type AudienceScope string

const (
	AudienceAnonymous             AudienceScope = "anonymous"
	AudienceOwner                 AudienceScope = "owner"
	AudienceAuthenticatedNonOwner AudienceScope = "authenticated_nonowner"
)

// SnapshotRevisionInput contains the authorized, visible snapshot and its
// effective query scope. DataUpdatedAt may be nil for a never-seen empty
// subject. GeneratedAt is intentionally excluded from the revision.
type SnapshotRevisionInput struct {
	Timeline       domain.Timeline
	From           domain.Date
	To             domain.Date
	Timezone       string
	EnvironmentIDs []domain.EnvironmentID
	Audience       AudienceScope
	DataUpdatedAt  *time.Time
	GeneratedAt    time.Time
}

// ComputeSnapshotRevision hashes a canonical representation of a snapshot.
// It does not access the clock, a provider, or persistence. Provider refresh
// status and warning text are not part of this value, so a failed refresh that
// leaves the authorized result unchanged also leaves the revision unchanged.
func ComputeSnapshotRevision(input SnapshotRevisionInput) string {
	selected := make([]domain.EnvironmentID, 0, len(input.EnvironmentIDs))
	seen := make(map[domain.EnvironmentID]struct{}, len(input.EnvironmentIDs))
	for _, id := range input.EnvironmentIDs {
		if _, exists := seen[id]; exists {
			continue
		}
		seen[id] = struct{}{}
		selected = append(selected, id)
	}
	sort.Slice(selected, func(i, j int) bool { return selected[i] < selected[j] })

	environments := append(make([]domain.Environment, 0, len(input.Timeline.Environments)), input.Timeline.Environments...)
	for index := range environments {
		if len(environments[index].Metadata) == 0 {
			environments[index].Metadata = nil
		}
	}
	sort.Slice(environments, func(i, j int) bool { return canonicalJSON(environments[i]) < canonicalJSON(environments[j]) })

	days := make([]domain.Day, 0, len(input.Timeline.Days))
	for _, original := range input.Timeline.Days {
		day := original
		day.Entries = append(make([]domain.DayEntry, 0, len(original.Entries)), original.Entries...)
		for index := range day.Entries {
			if len(day.Entries[index].Metadata) == 0 {
				day.Entries[index].Metadata = nil
			}
		}
		sort.Slice(day.Entries, func(i, j int) bool { return canonicalJSON(day.Entries[i]) < canonicalJSON(day.Entries[j]) })
		days = append(days, day)
	}
	sort.Slice(days, func(i, j int) bool { return canonicalJSON(days[i]) < canonicalJSON(days[j]) })

	var dataUpdatedAt *string
	if input.DataUpdatedAt != nil {
		formatted := input.DataUpdatedAt.UTC().Format(time.RFC3339Nano)
		dataUpdatedAt = &formatted
	}
	payload := struct {
		Version        int
		Subject        domain.SubjectID
		From           domain.Date
		To             domain.Date
		Timezone       string
		ResultTimezone string
		Audience       AudienceScope
		Selected       []domain.EnvironmentID
		DataUpdatedAt  *string
		Environments   []domain.Environment
		Days           []domain.Day
	}{
		Version:        1,
		Subject:        input.Timeline.Subject,
		From:           input.From,
		To:             input.To,
		Timezone:       input.Timezone,
		ResultTimezone: input.Timeline.Timezone,
		Audience:       input.Audience,
		Selected:       selected,
		DataUpdatedAt:  dataUpdatedAt,
		Environments:   environments,
		Days:           days,
	}
	digest := sha256.Sum256([]byte(canonicalJSON(payload)))
	return "v1:" + hex.EncodeToString(digest[:])
}

func canonicalJSON(value any) string {
	encoded, err := json.Marshal(value)
	if err != nil {
		// The revision input uses only JSON-compatible domain value types.
		panic(fmt.Errorf("activity snapshot revision: canonical JSON: %w", err))
	}
	return string(encoded)
}
