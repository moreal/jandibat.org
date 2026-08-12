// Package activity coordinates activity providers, persistence, cache policy,
// and the pure domain timeline projection.
package activity

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	domain "github.com/moreal/jandibat.org/apps/api/internal/domain/activity"
	"golang.org/x/sync/singleflight"
)

const (
	defaultTimezone                 = "UTC"
	defaultRangeDays                = 365
	defaultMaxDays                  = 366
	defaultGlobalFetchConcurrency   = 16
	defaultProviderFetchConcurrency = 4
)

var (
	ErrNilStore            = errors.New("activity application: store is required")
	ErrInvalidDate         = errors.New("activity application: invalid date")
	ErrInvalidDateRange    = errors.New("activity application: from must not be after to")
	ErrDateRangeTooLarge   = errors.New("activity application: date range is too large")
	ErrInvalidProvider     = errors.New("activity application: invalid provider")
	ErrProviderFact        = errors.New("activity application: provider returned an invalid fact")
	ErrProviderUnavailable = errors.New("activity application: provider unavailable and no stale facts exist")
	ErrProviderSaturated   = errors.New("activity application: provider fetch concurrency saturated")
)

// Store extends the domain persistence contract with the two operations needed
// to implement range-safe refreshes. ReplaceFacts replaces facts only inside the
// supplied range and environment set; other facts must remain untouched.
type Store interface {
	domain.ActivityStore
	ReplaceFacts(
		ctx context.Context,
		filter domain.LoadFactsInput,
		environmentIDs []domain.EnvironmentID,
		facts []domain.Fact,
	) error
	LoadCachedAt(
		ctx context.Context,
		subject domain.SubjectID,
		environmentID domain.EnvironmentID,
		date domain.Date,
	) (*time.Time, error)
	SaveCachedAt(
		ctx context.Context,
		subject domain.SubjectID,
		environmentID domain.EnvironmentID,
		dates []domain.Date,
		at time.Time,
	) error
	DeleteCachedAt(
		ctx context.Context,
		subject domain.SubjectID,
		environmentID domain.EnvironmentID,
		dates []domain.Date,
	) error
}

type ProviderFetchInput struct {
	// Subject is the local persistence identity. ProviderSubject is the public
	// forge login used only for outbound URLs; it defaults to Subject for
	// unmanaged public lookups.
	Subject         domain.SubjectID
	ProviderSubject domain.SubjectID
	Timezone        string
	From            domain.Date
	To              domain.Date
}

// Provider is an outbound application port. Each provider owns exactly one
// Environment, which makes refresh and failure handling independently scoped.
type Provider interface {
	Environment() domain.Environment
	Fetch(ctx context.Context, input ProviderFetchInput) ([]domain.Fact, error)
}

// SubjectTimelineSettingsReader supplies defaults for managed subjects without
// coupling the activity application layer to the subjects persistence model.
// The bool is false for an unmanaged public provider identifier.
type SubjectTimelineSettingsReader interface {
	LoadSubjectTimelineSettings(context.Context, domain.SubjectID) (SubjectTimelineSettings, bool, error)
}

type SubjectTimelineSettings struct {
	Timezone      string
	FailurePolicy domain.FetchFailurePolicy
}

// SubjectExistenceReader lets persistence adapters distinguish an existing
// subject with an empty activity range from an arbitrary, never-seen public
// identifier. It is deliberately optional so lightweight Store adapters do not
// have to materialize subjects merely to serve public activity.
type SubjectExistenceReader interface {
	SubjectExists(context.Context, domain.SubjectID) (bool, error)
}

type GetTimelineOptions struct {
	DefaultTimezone          string
	DefaultRangeDays         int
	MaxRangeDays             int
	CachePolicy              *domain.CachePolicy
	FailurePolicy            domain.FetchFailurePolicy
	SubjectSettings          SubjectTimelineSettingsReader
	GlobalFetchConcurrency   int
	ProviderFetchConcurrency int
	Now                      func() time.Time
}

type GetTimelineInput struct {
	Subject                    domain.SubjectID
	ProviderSubject            domain.SubjectID
	Timezone                   string
	From                       *domain.Date
	To                         *domain.Date
	Force                      bool
	FailurePolicy              domain.FetchFailurePolicy
	EnvironmentIDs             []domain.EnvironmentID
	IncludeSubjectEnvironments bool
}

type ProviderWarning struct {
	Provider string
	Err      error
}

type GetTimelineOutput struct {
	Timeline  domain.Timeline
	From      domain.Date
	To        domain.Date
	Refreshed bool
	Stale     bool
	Warnings  []ProviderWarning
}

type GetTimeline struct {
	store              Store
	providers          []Provider
	defaultTimezone    string
	defaultRangeDays   int
	maxRangeDays       int
	cachePolicy        domain.CachePolicy
	failurePolicy      domain.FetchFailurePolicy
	subjectSettings    SubjectTimelineSettingsReader
	globalFetchSlots   chan struct{}
	providerFetchSlots map[domain.EnvironmentID]chan struct{}
	fetches            singleflight.Group
	now                func() time.Time
}

func NewGetTimeline(store Store, providers []Provider, options GetTimelineOptions) (*GetTimeline, error) {
	if store == nil {
		return nil, ErrNilStore
	}

	timezone := options.DefaultTimezone
	if timezone == "" {
		timezone = defaultTimezone
	}
	rangeDays := options.DefaultRangeDays
	if rangeDays <= 0 {
		rangeDays = defaultRangeDays
	}
	maxDays := options.MaxRangeDays
	if maxDays <= 0 {
		maxDays = defaultMaxDays
	}
	policy := domain.CachePolicy{HotTTL: 5 * time.Minute, ColdTTL: 0}
	if options.CachePolicy != nil {
		policy = *options.CachePolicy
	}
	if err := domain.ValidateCachePolicy(policy); err != nil {
		return nil, err
	}
	failurePolicy := options.FailurePolicy
	if failurePolicy == "" {
		failurePolicy = domain.FetchFailureKeepStale
	}
	if _, err := domain.ResolveFactsOnFetchFailure(nil, failurePolicy); err != nil {
		return nil, err
	}
	now := options.Now
	if now == nil {
		now = time.Now
	}

	orderedProviders := append([]Provider(nil), providers...)
	for _, provider := range orderedProviders {
		if provider == nil || provider.Environment().ID == "" {
			return nil, ErrInvalidProvider
		}
	}
	sort.SliceStable(orderedProviders, func(i, j int) bool {
		return orderedProviders[i].Environment().ID < orderedProviders[j].Environment().ID
	})
	for i := 1; i < len(orderedProviders); i++ {
		if orderedProviders[i-1].Environment().ID == orderedProviders[i].Environment().ID {
			return nil, fmt.Errorf("%w: duplicate environment %q", ErrInvalidProvider, orderedProviders[i].Environment().ID)
		}
	}
	globalFetchConcurrency := options.GlobalFetchConcurrency
	if globalFetchConcurrency <= 0 {
		globalFetchConcurrency = defaultGlobalFetchConcurrency
	}
	providerFetchConcurrency := options.ProviderFetchConcurrency
	if providerFetchConcurrency <= 0 {
		providerFetchConcurrency = defaultProviderFetchConcurrency
	}
	providerFetchSlots := make(map[domain.EnvironmentID]chan struct{}, len(orderedProviders))
	for _, provider := range orderedProviders {
		providerFetchSlots[provider.Environment().ID] = make(chan struct{}, providerFetchConcurrency)
	}

	return &GetTimeline{
		store:              store,
		providers:          orderedProviders,
		defaultTimezone:    timezone,
		defaultRangeDays:   rangeDays,
		maxRangeDays:       maxDays,
		cachePolicy:        policy,
		failurePolicy:      failurePolicy,
		subjectSettings:    options.SubjectSettings,
		globalFetchSlots:   make(chan struct{}, globalFetchConcurrency),
		providerFetchSlots: providerFetchSlots,
		now:                now,
	}, nil
}

func (usecase *GetTimeline) Execute(ctx context.Context, input GetTimelineInput) (GetTimelineOutput, error) {
	if input.Subject == "" {
		return GetTimelineOutput{}, domain.ErrEmptySubject
	}

	timezone, failurePolicy, err := usecase.resolveSubjectSettings(ctx, input)
	if err != nil {
		return GetTimelineOutput{}, err
	}
	location, err := time.LoadLocation(timezone)
	if err != nil {
		return GetTimelineOutput{}, fmt.Errorf("%w: timezone %q: %v", ErrInvalidDate, timezone, err)
	}
	now := usecase.now()
	from, to, dates, err := usecase.resolveRange(input.From, input.To, now.In(location))
	if err != nil {
		return GetTimelineOutput{}, err
	}

	if _, err := domain.ResolveFactsOnFetchFailure(nil, failurePolicy); err != nil {
		return GetTimelineOutput{}, err
	}

	output := GetTimelineOutput{From: from, To: to}
	today := domain.Date(now.In(location).Format(time.DateOnly))
	filter := domain.LoadFactsInput{Subject: input.Subject, From: &from, To: &to}

	selected := make(map[domain.EnvironmentID]struct{}, len(input.EnvironmentIDs))
	for _, id := range input.EnvironmentIDs {
		if id == "" {
			return GetTimelineOutput{}, ErrInvalidProvider
		}
		selected[id] = struct{}{}
	}
	for _, provider := range usecase.providers {
		environment := provider.Environment()
		if len(selected) != 0 {
			if _, ok := selected[environment.ID]; !ok {
				continue
			}
		}
		needsRefresh, err := usecase.needsRefresh(ctx, input.Subject, environment.ID, dates, today, now, input.Force)
		if err != nil {
			return GetTimelineOutput{}, err
		}
		if !needsRefresh {
			continue
		}

		providerSubject := input.ProviderSubject
		if providerSubject == "" {
			providerSubject = input.Subject
		}
		fetchErr := usecase.refreshProvider(ctx, provider, providerSubject, filter, dates, now, timezone, from, to)
		if fetchErr != nil {
			output.Warnings = append(output.Warnings, ProviderWarning{
				Provider: environment.Key,
				Err:      fetchErr,
			})
			switch failurePolicy {
			case domain.FetchFailureKeepStale:
				output.Stale = true
			case domain.FetchFailurePurge:
				if err := usecase.store.ReplaceFacts(ctx, filter, []domain.EnvironmentID{environment.ID}, nil); err != nil {
					return GetTimelineOutput{}, err
				}
				if err := usecase.store.DeleteCachedAt(ctx, input.Subject, environment.ID, dates); err != nil {
					return GetTimelineOutput{}, err
				}
			}
			continue
		}

		output.Refreshed = true
	}

	facts, err := usecase.store.LoadFacts(ctx, filter)
	if err != nil {
		return GetTimelineOutput{}, err
	}
	if len(selected) != 0 {
		filtered := facts[:0]
		for _, fact := range facts {
			if _, ok := selected[fact.EnvironmentID]; ok {
				filtered = append(filtered, fact)
			}
		}
		facts = filtered
	}
	if err := unavailableProviderError(output.Warnings, facts); err != nil {
		return GetTimelineOutput{}, err
	}
	environmentIDs := environmentIDsForFacts(facts)
	environments, err := usecase.store.LoadEnvironments(ctx, domain.LoadEnvironmentsInput{IDs: environmentIDs})
	if err != nil {
		return GetTimelineOutput{}, err
	}
	environments, facts = filterVisibleEnvironments(input.Subject, input.IncludeSubjectEnvironments, environments, facts)
	timeline, err := domain.BuildTimelineFromFacts(input.Subject, timezone, environments, facts)
	if err != nil {
		return GetTimelineOutput{}, err
	}
	output.Timeline = timeline
	return output, nil
}

func (usecase *GetTimeline) refreshProvider(
	ctx context.Context,
	provider Provider,
	providerSubject domain.SubjectID,
	filter domain.LoadFactsInput,
	dates []domain.Date,
	now time.Time,
	timezone string,
	from, to domain.Date,
) error {
	environment := provider.Environment()
	key := fetchKey(filter.Subject, providerSubject, environment.ID, timezone, from, to)
	result := usecase.fetches.DoChan(key, func() (any, error) {
		if err := usecase.acquireFetchSlots(ctx, environment.ID); err != nil {
			return nil, err
		}
		defer usecase.releaseFetchSlots(environment.ID)

		facts, err := provider.Fetch(ctx, ProviderFetchInput{
			Subject: filter.Subject, ProviderSubject: providerSubject, Timezone: timezone, From: from, To: to,
		})
		if err != nil {
			return nil, err
		}
		if err := validateProviderFacts(filter.Subject, environment.ID, from, to, facts); err != nil {
			return nil, err
		}
		if len(facts) == 0 {
			exists, err := usecase.subjectExists(ctx, filter.Subject)
			if err != nil {
				return nil, err
			}
			if !exists {
				// A successful empty lookup must not turn an attacker-controlled
				// random handle into durable subject/cache rows. Concurrent misses
				// are still coalesced by the surrounding singleflight.
				return nil, nil
			}
		}
		if err := usecase.store.SaveEnvironments(ctx, domain.SaveEnvironmentsInput{
			Environments: []domain.Environment{environment},
		}); err != nil {
			return nil, err
		}
		if err := usecase.store.ReplaceFacts(ctx, filter, []domain.EnvironmentID{environment.ID}, facts); err != nil {
			return nil, err
		}
		if err := usecase.store.SaveCachedAt(ctx, filter.Subject, environment.ID, dates, now); err != nil {
			return nil, err
		}
		return nil, nil
	})
	select {
	case <-ctx.Done():
		return ctx.Err()
	case completed := <-result:
		return completed.Err
	}
}

func (usecase *GetTimeline) subjectExists(ctx context.Context, subject domain.SubjectID) (bool, error) {
	reader, ok := usecase.store.(SubjectExistenceReader)
	if !ok {
		return false, nil
	}
	return reader.SubjectExists(ctx, subject)
}

func (usecase *GetTimeline) acquireFetchSlots(ctx context.Context, environmentID domain.EnvironmentID) error {
	select {
	case usecase.globalFetchSlots <- struct{}{}:
	case <-ctx.Done():
		return ctx.Err()
	default:
		return ErrProviderSaturated
	}
	providerSlots := usecase.providerFetchSlots[environmentID]
	select {
	case providerSlots <- struct{}{}:
		return nil
	case <-ctx.Done():
		<-usecase.globalFetchSlots
		return ctx.Err()
	default:
		<-usecase.globalFetchSlots
		return ErrProviderSaturated
	}
}

func (usecase *GetTimeline) releaseFetchSlots(environmentID domain.EnvironmentID) {
	<-usecase.providerFetchSlots[environmentID]
	<-usecase.globalFetchSlots
}

func fetchKey(subject, providerSubject domain.SubjectID, environmentID domain.EnvironmentID, timezone string, from, to domain.Date) string {
	parts := []string{string(subject), string(providerSubject), string(environmentID), timezone, string(from), string(to)}
	var key strings.Builder
	for _, part := range parts {
		key.WriteString(strconv.Itoa(len(part)))
		key.WriteByte(':')
		key.WriteString(part)
	}
	return key.String()
}

func (usecase *GetTimeline) resolveSubjectSettings(ctx context.Context, input GetTimelineInput) (string, domain.FetchFailurePolicy, error) {
	timezone := input.Timezone
	failurePolicy := input.FailurePolicy
	if (timezone == "" || failurePolicy == "") && usecase.subjectSettings != nil {
		settings, found, err := usecase.subjectSettings.LoadSubjectTimelineSettings(ctx, input.Subject)
		if err != nil {
			return "", "", fmt.Errorf("load subject timeline settings: %w", err)
		}
		if found {
			if timezone == "" {
				timezone = settings.Timezone
			}
			if failurePolicy == "" {
				failurePolicy = settings.FailurePolicy
			}
		}
	}
	if timezone == "" {
		timezone = usecase.defaultTimezone
	}
	if failurePolicy == "" {
		failurePolicy = usecase.failurePolicy
	}
	return timezone, failurePolicy, nil
}

func unavailableProviderError(warnings []ProviderWarning, facts []domain.Fact) error {
	if len(warnings) == 0 {
		return nil
	}
	available := make(map[string]struct{}, len(facts))
	for _, fact := range facts {
		available[string(fact.EnvironmentID)] = struct{}{}
	}
	failures := make([]error, 0, len(warnings))
	for _, warning := range warnings {
		if _, hasStaleFacts := available[warning.Provider]; !hasStaleFacts {
			failures = append(failures, warning.Err)
		}
	}
	if len(failures) == 0 {
		return nil
	}
	return fmt.Errorf("%w: %w", ErrProviderUnavailable, errors.Join(failures...))
}

// filterVisibleEnvironments is intentionally fail-closed on subject ownership.
// Subject scope alone is not private: custom-provider activity belonging to a
// public subject remains public. Credential-backed connection environments opt
// out of public projection with explicit visibility metadata.
func filterVisibleEnvironments(
	subject domain.SubjectID,
	includeSubjectEnvironments bool,
	environments []domain.Environment,
	facts []domain.Fact,
) ([]domain.Environment, []domain.Fact) {
	allowed := make(map[domain.EnvironmentID]struct{}, len(environments))
	visibleEnvironments := make([]domain.Environment, 0, len(environments))
	for _, environment := range environments {
		visible := environment.Scope == domain.EnvironmentScopeGlobal
		if environment.Scope == domain.EnvironmentScopeSubject && environment.OwnerSubject != nil && *environment.OwnerSubject == subject {
			visible = environment.Metadata["visibility"] != "private" || includeSubjectEnvironments
		}
		if !visible {
			continue
		}
		allowed[environment.ID] = struct{}{}
		visibleEnvironments = append(visibleEnvironments, environment)
	}
	visibleFacts := make([]domain.Fact, 0, len(facts))
	for _, fact := range facts {
		if _, ok := allowed[fact.EnvironmentID]; ok {
			visibleFacts = append(visibleFacts, fact)
		}
	}
	return visibleEnvironments, visibleFacts
}

func (usecase *GetTimeline) needsRefresh(
	ctx context.Context,
	subject domain.SubjectID,
	environmentID domain.EnvironmentID,
	dates []domain.Date,
	today domain.Date,
	now time.Time,
	force bool,
) (bool, error) {
	for _, date := range dates {
		cachedAt, err := usecase.store.LoadCachedAt(ctx, subject, environmentID, date)
		if err != nil {
			return false, err
		}
		refresh, err := domain.ShouldRefresh(now, cachedAt, date, today, usecase.cachePolicy, force)
		if err != nil {
			return false, err
		}
		if refresh {
			return true, nil
		}
	}
	return false, nil
}

func (usecase *GetTimeline) resolveRange(fromInput, toInput *domain.Date, now time.Time) (domain.Date, domain.Date, []domain.Date, error) {
	today := now.Format(time.DateOnly)
	toText := today
	if toInput != nil {
		toText = string(*toInput)
	}
	toTime, err := time.Parse(time.DateOnly, toText)
	if err != nil {
		return "", "", nil, fmt.Errorf("%w: to %q", ErrInvalidDate, toText)
	}

	fromText := ""
	if fromInput != nil {
		fromText = string(*fromInput)
	} else {
		fromText = toTime.AddDate(0, 0, -(usecase.defaultRangeDays - 1)).Format(time.DateOnly)
	}
	fromTime, err := time.Parse(time.DateOnly, fromText)
	if err != nil {
		return "", "", nil, fmt.Errorf("%w: from %q", ErrInvalidDate, fromText)
	}
	if fromTime.After(toTime) {
		return "", "", nil, ErrInvalidDateRange
	}

	days := int(toTime.Sub(fromTime).Hours()/24) + 1
	if days > usecase.maxRangeDays {
		return "", "", nil, fmt.Errorf("%w: %d days (maximum %d)", ErrDateRangeTooLarge, days, usecase.maxRangeDays)
	}
	dates := make([]domain.Date, 0, days)
	for date := fromTime; !date.After(toTime); date = date.AddDate(0, 0, 1) {
		dates = append(dates, domain.Date(date.Format(time.DateOnly)))
	}
	return domain.Date(fromText), domain.Date(toText), dates, nil
}

func validateProviderFacts(subject domain.SubjectID, environmentID domain.EnvironmentID, from, to domain.Date, facts []domain.Fact) error {
	for _, fact := range facts {
		if fact.Subject != subject || fact.EnvironmentID != environmentID || fact.Date < from || fact.Date > to {
			return fmt.Errorf("%w: environment %q, date %q", ErrProviderFact, fact.EnvironmentID, fact.Date)
		}
	}
	return nil
}

func environmentIDsForFacts(facts []domain.Fact) []domain.EnvironmentID {
	set := make(map[domain.EnvironmentID]struct{}, len(facts))
	for _, fact := range facts {
		set[fact.EnvironmentID] = struct{}{}
	}
	ids := make([]domain.EnvironmentID, 0, len(set))
	for id := range set {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	return ids
}
