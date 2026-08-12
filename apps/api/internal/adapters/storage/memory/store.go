// Package memory provides a concurrency-safe in-memory activity store.
package memory

import (
	"context"
	"encoding/json"
	"errors"
	"sort"
	"strings"
	"sync"
	"time"

	domain "github.com/moreal/jandibat.org/apps/api/internal/domain/activity"
)

var (
	ErrEmptySubject       = errors.New("memory activity store: subject is required")
	ErrMismatchedSubject  = errors.New("memory activity store: fact subject mismatch")
	ErrFactOutsideRange   = errors.New("memory activity store: fact is outside replacement range")
	ErrEnvironmentOutside = errors.New("memory activity store: fact environment is outside replacement set")
)

type cacheKey struct {
	subject       domain.SubjectID
	environmentID domain.EnvironmentID
	date          domain.Date
}

// Store keeps snapshots private by cloning all mutable values at its boundary.
// Its zero value is ready for use.
type Store struct {
	mu           sync.RWMutex
	facts        map[domain.SubjectID][]domain.Fact
	environments map[domain.EnvironmentID]domain.Environment
	cachedAt     map[cacheKey]time.Time
}

func New() *Store {
	return &Store{
		facts:        make(map[domain.SubjectID][]domain.Fact),
		environments: make(map[domain.EnvironmentID]domain.Environment),
		cachedAt:     make(map[cacheKey]time.Time),
	}
}

// SaveFacts upserts facts using the same logical identity as the database
// dedupe key: subject, environment, date, action, metric name, and metadata.
func (store *Store) SaveFacts(ctx context.Context, input domain.SaveFactsInput) error {
	if err := contextError(ctx); err != nil {
		return err
	}
	if input.Subject == "" {
		return ErrEmptySubject
	}
	for _, fact := range input.Facts {
		if fact.Subject != input.Subject {
			return ErrMismatchedSubject
		}
	}

	store.mu.Lock()
	defer store.mu.Unlock()
	store.initializeLocked()
	facts := store.facts[input.Subject]
	index := make(map[string]int, len(facts)+len(input.Facts))
	for i, fact := range facts {
		index[factIdentity(fact)] = i
	}
	for _, fact := range input.Facts {
		cloned := cloneFact(fact)
		key := factIdentity(cloned)
		if position, ok := index[key]; ok {
			facts[position] = cloned
			continue
		}
		index[key] = len(facts)
		facts = append(facts, cloned)
	}
	store.facts[input.Subject] = facts
	return nil
}

func (store *Store) LoadFacts(ctx context.Context, input domain.LoadFactsInput) ([]domain.Fact, error) {
	if err := contextError(ctx); err != nil {
		return nil, err
	}
	if input.Subject == "" {
		return nil, ErrEmptySubject
	}

	store.mu.RLock()
	defer store.mu.RUnlock()
	facts := store.facts[input.Subject]
	result := make([]domain.Fact, 0, len(facts))
	for _, fact := range facts {
		if dateInRange(fact.Date, input.From, input.To) {
			result = append(result, cloneFact(fact))
		}
	}
	sortFacts(result)
	return result, nil
}

// SubjectExists reports whether activity persistence has already seen a
// subject. Empty public lookups intentionally do not make a subject exist.
func (store *Store) SubjectExists(ctx context.Context, subject domain.SubjectID) (bool, error) {
	if err := contextError(ctx); err != nil {
		return false, err
	}
	if subject == "" {
		return false, ErrEmptySubject
	}
	store.mu.RLock()
	defer store.mu.RUnlock()
	if _, ok := store.facts[subject]; ok {
		return true, nil
	}
	for key := range store.cachedAt {
		if key.subject == subject {
			return true, nil
		}
	}
	return false, nil
}

// ReplaceFacts atomically replaces only facts in filter's date range and the
// supplied environments. An empty environment list means all environments.
func (store *Store) ReplaceFacts(
	ctx context.Context,
	filter domain.LoadFactsInput,
	environmentIDs []domain.EnvironmentID,
	facts []domain.Fact,
) error {
	if err := contextError(ctx); err != nil {
		return err
	}
	if filter.Subject == "" {
		return ErrEmptySubject
	}
	environmentSet := make(map[domain.EnvironmentID]struct{}, len(environmentIDs))
	for _, id := range environmentIDs {
		environmentSet[id] = struct{}{}
	}
	for _, fact := range facts {
		if fact.Subject != filter.Subject {
			return ErrMismatchedSubject
		}
		if !dateInRange(fact.Date, filter.From, filter.To) {
			return ErrFactOutsideRange
		}
		if len(environmentSet) > 0 {
			if _, ok := environmentSet[fact.EnvironmentID]; !ok {
				return ErrEnvironmentOutside
			}
		}
	}

	store.mu.Lock()
	defer store.mu.Unlock()
	store.initializeLocked()
	existing := store.facts[filter.Subject]
	replaced := make([]domain.Fact, 0, len(existing)+len(facts))
	for _, fact := range existing {
		_, environmentMatches := environmentSet[fact.EnvironmentID]
		if len(environmentSet) == 0 {
			environmentMatches = true
		}
		if environmentMatches && dateInRange(fact.Date, filter.From, filter.To) {
			continue
		}
		replaced = append(replaced, fact)
	}
	for _, fact := range facts {
		replaced = append(replaced, cloneFact(fact))
	}
	store.facts[filter.Subject] = replaced
	return nil
}

func (store *Store) SaveEnvironments(ctx context.Context, input domain.SaveEnvironmentsInput) error {
	if err := contextError(ctx); err != nil {
		return err
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	store.initializeLocked()
	for _, environment := range input.Environments {
		store.environments[environment.ID] = cloneEnvironment(environment)
	}
	return nil
}

func (store *Store) LoadEnvironments(ctx context.Context, input domain.LoadEnvironmentsInput) ([]domain.Environment, error) {
	if err := contextError(ctx); err != nil {
		return nil, err
	}
	store.mu.RLock()
	defer store.mu.RUnlock()

	result := make([]domain.Environment, 0, len(input.IDs))
	if len(input.IDs) == 0 {
		result = make([]domain.Environment, 0, len(store.environments))
		for _, environment := range store.environments {
			result = append(result, cloneEnvironment(environment))
		}
	} else {
		seen := make(map[domain.EnvironmentID]struct{}, len(input.IDs))
		for _, id := range input.IDs {
			if _, duplicate := seen[id]; duplicate {
				continue
			}
			seen[id] = struct{}{}
			if environment, ok := store.environments[id]; ok {
				result = append(result, cloneEnvironment(environment))
			}
		}
	}
	sort.Slice(result, func(i, j int) bool { return result[i].ID < result[j].ID })
	return result, nil
}

func (store *Store) LoadCachedAt(
	ctx context.Context,
	subject domain.SubjectID,
	environmentID domain.EnvironmentID,
	date domain.Date,
) (*time.Time, error) {
	if err := contextError(ctx); err != nil {
		return nil, err
	}
	store.mu.RLock()
	defer store.mu.RUnlock()
	at, ok := store.cachedAt[cacheKey{subject: subject, environmentID: environmentID, date: date}]
	if !ok {
		return nil, nil
	}
	copy := at
	return &copy, nil
}

func (store *Store) SaveCachedAt(
	ctx context.Context,
	subject domain.SubjectID,
	environmentID domain.EnvironmentID,
	dates []domain.Date,
	at time.Time,
) error {
	if err := contextError(ctx); err != nil {
		return err
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	store.initializeLocked()
	for _, date := range dates {
		store.cachedAt[cacheKey{subject: subject, environmentID: environmentID, date: date}] = at
	}
	return nil
}

func (store *Store) DeleteCachedAt(
	ctx context.Context,
	subject domain.SubjectID,
	environmentID domain.EnvironmentID,
	dates []domain.Date,
) error {
	if err := contextError(ctx); err != nil {
		return err
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	for _, date := range dates {
		delete(store.cachedAt, cacheKey{subject: subject, environmentID: environmentID, date: date})
	}
	return nil
}

func (store *Store) initializeLocked() {
	if store.facts == nil {
		store.facts = make(map[domain.SubjectID][]domain.Fact)
	}
	if store.environments == nil {
		store.environments = make(map[domain.EnvironmentID]domain.Environment)
	}
	if store.cachedAt == nil {
		store.cachedAt = make(map[cacheKey]time.Time)
	}
}

func contextError(ctx context.Context) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
		return nil
	}
}

func dateInRange(date domain.Date, from, to *domain.Date) bool {
	return (from == nil || date >= *from) && (to == nil || date <= *to)
}

func factIdentity(fact domain.Fact) string {
	metadata, _ := json.Marshal(fact.Metadata)
	return strings.Join([]string{
		string(fact.Subject), string(fact.EnvironmentID), string(fact.Date),
		string(fact.Action), string(fact.Metric.Name), string(metadata),
	}, "\x00")
}

func sortFacts(facts []domain.Fact) {
	sort.Slice(facts, func(i, j int) bool {
		left := factIdentity(facts[i])
		right := factIdentity(facts[j])
		if left != right {
			return left < right
		}
		return facts[i].Metric.Value < facts[j].Metric.Value
	})
}

func cloneFact(fact domain.Fact) domain.Fact {
	cloned := fact
	cloned.Metadata = cloneMetadata(fact.Metadata)
	return cloned
}

func cloneEnvironment(environment domain.Environment) domain.Environment {
	cloned := environment
	if environment.OwnerSubject != nil {
		owner := *environment.OwnerSubject
		cloned.OwnerSubject = &owner
	}
	cloned.Metadata = cloneMetadata(environment.Metadata)
	return cloned
}

func cloneMetadata(metadata map[string]string) map[string]string {
	if metadata == nil {
		return nil
	}
	cloned := make(map[string]string, len(metadata))
	for key, value := range metadata {
		cloned[key] = value
	}
	return cloned
}
