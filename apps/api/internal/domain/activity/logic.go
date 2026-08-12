package activity

import (
	"errors"
	"sort"
	"time"
)

var (
	ErrEmptySubject               = errors.New("activity: subject is required")
	ErrEmptyTimezone              = errors.New("activity: timezone is required")
	ErrEmptyFactDate              = errors.New("activity: fact date is required")
	ErrEmptyEnvironmentID         = errors.New("activity: environment id is required")
	ErrUnknownEnvironmentID       = errors.New("activity: unknown environment id")
	ErrEmptyEnvironmentKey        = errors.New("activity: environment key is required")
	ErrEmptyEnvironmentName       = errors.New("activity: environment name is required")
	ErrInvalidEnvironmentScope    = errors.New("activity: invalid environment scope")
	ErrEmptyAction                = errors.New("activity: action is required")
	ErrEmptyMetricName            = errors.New("activity: metric name is required")
	ErrNegativeMetricValue        = errors.New("activity: metric value must be >= 0")
	ErrMetricAggregationOverflow  = errors.New("activity: metric aggregation exceeds integer range")
	ErrMismatchedFactSubject      = errors.New("activity: fact subject mismatch")
	ErrNegativeHotTTL             = errors.New("activity: hot ttl must be >= 0")
	ErrNegativeColdTTL            = errors.New("activity: cold ttl must be >= 0")
	ErrInvalidFetchFailurePolicy  = errors.New("activity: invalid fetch failure policy")
	ErrDuplicateEnvironmentID     = errors.New("activity: duplicate environment id")
	ErrMissingEnvironmentOwner    = errors.New("activity: owner subject is required for subject-scoped environment")
	ErrUnexpectedEnvironmentOwner = errors.New("activity: owner subject must be empty for global environment")
)

// LevelForCount converts raw contribution count into a normalized heatmap level.
func LevelForCount(count int) Level {
	switch {
	case count <= 0:
		return LevelNone
	case count <= 2:
		return LevelLow
	case count <= 5:
		return LevelMedium
	case count <= 9:
		return LevelHigh
	default:
		return LevelVeryHigh
	}
}

// AddMetricValues adds non-negative metric values without allowing integer
// wraparound. Callers use the returned error instead of emitting a corrupted
// negative activity count when persisted facts accumulate beyond int range.
func AddMetricValues(total, value int) (int, error) {
	if total < 0 || value < 0 {
		return 0, ErrNegativeMetricValue
	}
	maximum := int(^uint(0) >> 1)
	if total > maximum-value {
		return 0, ErrMetricAggregationOverflow
	}
	return total + value, nil
}

// BuildTimelineFromFacts projects fact records into a daily heatmap timeline.
// Facts must reference environments by ID, and referenced environments must exist.
func BuildTimelineFromFacts(subject SubjectID, timezone string, environments []Environment, facts []Fact) (Timeline, error) {
	if subject == "" {
		return Timeline{}, ErrEmptySubject
	}

	if timezone == "" {
		return Timeline{}, ErrEmptyTimezone
	}

	environmentIndex, err := buildEnvironmentIndex(environments)
	if err != nil {
		return Timeline{}, err
	}

	type dayAggregate struct {
		count   int
		entries []DayEntry
	}

	aggregates := make(map[Date]*dayAggregate, len(facts))
	usedEnvironmentIDs := make(map[EnvironmentID]struct{}, len(facts))
	for _, rawFact := range facts {
		fact, err := normalizeFact(subject, rawFact, environmentIndex)
		if err != nil {
			return Timeline{}, err
		}

		day, ok := aggregates[fact.Date]
		if !ok {
			day = &dayAggregate{}
			aggregates[fact.Date] = day
		}

		day.count, err = AddMetricValues(day.count, fact.Metric.Value)
		if err != nil {
			return Timeline{}, err
		}
		day.entries = append(day.entries, DayEntry{
			EnvironmentID: fact.EnvironmentID,
			Action:        fact.Action,
			Metric:        fact.Metric,
			Metadata:      copyMetadata(fact.Metadata),
		})
		usedEnvironmentIDs[fact.EnvironmentID] = struct{}{}
	}

	dates := make([]Date, 0, len(aggregates))
	for date := range aggregates {
		dates = append(dates, date)
	}
	sort.Slice(dates, func(i, j int) bool {
		return dates[i] < dates[j]
	})

	days := make([]Day, 0, len(dates))
	for _, date := range dates {
		entries := append([]DayEntry(nil), aggregates[date].entries...)
		sort.Slice(entries, func(i, j int) bool {
			if entries[i].EnvironmentID != entries[j].EnvironmentID {
				return entries[i].EnvironmentID < entries[j].EnvironmentID
			}
			if entries[i].Action != entries[j].Action {
				return entries[i].Action < entries[j].Action
			}
			return entries[i].Metric.Name < entries[j].Metric.Name
		})

		count := aggregates[date].count
		days = append(days, Day{
			Date:    date,
			Count:   count,
			Level:   LevelForCount(count),
			Entries: entries,
		})
	}

	usedEnvironments := make([]Environment, 0, len(usedEnvironmentIDs))
	for id := range usedEnvironmentIDs {
		usedEnvironments = append(usedEnvironments, cloneEnvironment(environmentIndex[id]))
	}
	sort.Slice(usedEnvironments, func(i, j int) bool {
		return usedEnvironments[i].ID < usedEnvironments[j].ID
	})

	return Timeline{
		Subject:      subject,
		Timezone:     timezone,
		Environments: usedEnvironments,
		Days:         days,
	}, nil
}

// ValidateCachePolicy checks if cache TTL values are valid.
func ValidateCachePolicy(policy CachePolicy) error {
	if policy.HotTTL < 0 {
		return ErrNegativeHotTTL
	}
	if policy.ColdTTL < 0 {
		return ErrNegativeColdTTL
	}
	return nil
}

func IsHotDate(target Date, today Date) bool {
	return target == today
}

func TTLForDate(target Date, today Date, policy CachePolicy) time.Duration {
	if IsHotDate(target, today) {
		return policy.HotTTL
	}
	return policy.ColdTTL
}

// ShouldRefresh decides whether the caller should fetch fresh data from providers.
func ShouldRefresh(now time.Time, cachedAt *time.Time, target Date, today Date, policy CachePolicy, force bool) (bool, error) {
	if err := ValidateCachePolicy(policy); err != nil {
		return false, err
	}
	if force {
		return true, nil
	}
	if cachedAt == nil {
		return true, nil
	}

	ttl := TTLForDate(target, today, policy)
	if ttl == 0 {
		return false, nil
	}

	return !cachedAt.Add(ttl).After(now), nil
}

// ResolveFactsOnFetchFailure applies caller-selected behavior when provider fetch fails.
func ResolveFactsOnFetchFailure(existing []Fact, policy FetchFailurePolicy) ([]Fact, error) {
	switch policy {
	case FetchFailureKeepStale:
		cloned := make([]Fact, len(existing))
		for i := range existing {
			cloned[i] = cloneFact(existing[i])
		}
		return cloned, nil
	case FetchFailurePurge:
		return nil, nil
	default:
		return nil, ErrInvalidFetchFailurePolicy
	}
}

func normalizeFact(subject SubjectID, fact Fact, environmentIndex map[EnvironmentID]Environment) (Fact, error) {
	if fact.Subject == "" {
		fact.Subject = subject
	}
	if fact.Subject != subject {
		return Fact{}, ErrMismatchedFactSubject
	}
	if fact.Date == "" {
		return Fact{}, ErrEmptyFactDate
	}
	if fact.EnvironmentID == "" {
		return Fact{}, ErrEmptyEnvironmentID
	}
	if _, ok := environmentIndex[fact.EnvironmentID]; !ok {
		return Fact{}, ErrUnknownEnvironmentID
	}
	if fact.Action == "" {
		return Fact{}, ErrEmptyAction
	}
	if fact.Metric.Name == "" {
		return Fact{}, ErrEmptyMetricName
	}
	if fact.Metric.Value < 0 {
		return Fact{}, ErrNegativeMetricValue
	}
	fact.Metadata = copyMetadata(fact.Metadata)
	return fact, nil
}

func buildEnvironmentIndex(environments []Environment) (map[EnvironmentID]Environment, error) {
	index := make(map[EnvironmentID]Environment, len(environments))
	for _, rawEnvironment := range environments {
		environment, err := normalizeEnvironment(rawEnvironment)
		if err != nil {
			return nil, err
		}
		if _, exists := index[environment.ID]; exists {
			return nil, ErrDuplicateEnvironmentID
		}
		index[environment.ID] = environment
	}
	return index, nil
}

func normalizeEnvironment(environment Environment) (Environment, error) {
	if environment.ID == "" {
		return Environment{}, ErrEmptyEnvironmentID
	}
	if environment.Key == "" {
		return Environment{}, ErrEmptyEnvironmentKey
	}
	if environment.Name == "" {
		return Environment{}, ErrEmptyEnvironmentName
	}
	switch environment.Scope {
	case EnvironmentScopeGlobal:
		if environment.OwnerSubject != nil {
			return Environment{}, ErrUnexpectedEnvironmentOwner
		}
	case EnvironmentScopeSubject:
		if environment.OwnerSubject == nil || *environment.OwnerSubject == "" {
			return Environment{}, ErrMissingEnvironmentOwner
		}
	default:
		return Environment{}, ErrInvalidEnvironmentScope
	}
	environment.Metadata = copyMetadata(environment.Metadata)
	return environment, nil
}

func cloneFact(fact Fact) Fact {
	cloned := fact
	cloned.Metadata = copyMetadata(fact.Metadata)
	return cloned
}

func cloneEnvironment(environment Environment) Environment {
	cloned := environment
	if environment.OwnerSubject != nil {
		owner := *environment.OwnerSubject
		cloned.OwnerSubject = &owner
	}
	cloned.Metadata = copyMetadata(environment.Metadata)
	return cloned
}

func copyMetadata(metadata map[string]string) map[string]string {
	if len(metadata) == 0 {
		return nil
	}
	cloned := make(map[string]string, len(metadata))
	for key, value := range metadata {
		cloned[key] = value
	}
	return cloned
}
