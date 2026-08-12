package integrations

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"time"

	"github.com/moreal/jandibat.org/apps/api/internal/domain/activity"
)

type SchedulerConfig struct {
	SyncInterval    time.Duration
	PollInterval    time.Duration
	MaxConcurrent   int
	SubjectSettings SubjectSyncSettingsReader
}

type Scheduler struct {
	connections ConnectionStore
	syncService *SyncService
	clock       Clock
	config      SchedulerConfig
}

type scheduledConnection struct {
	id       string
	settings SubjectSyncSettings
}

func NewScheduler(connections ConnectionStore, syncService *SyncService, clock Clock, config SchedulerConfig) (*Scheduler, error) {
	if connections == nil {
		return nil, ErrMissingStore
	}
	if syncService == nil {
		return nil, ErrProviderSyncerNotFound
	}
	if config.SyncInterval <= 0 || config.PollInterval <= 0 {
		return nil, ErrInvalidSyncInterval
	}
	if config.MaxConcurrent <= 0 {
		return nil, ErrInvalidSchedulerConcurrency
	}
	return &Scheduler{
		connections: connections,
		syncService: syncService,
		clock:       clockOrDefault(clock),
		config:      config,
	}, nil
}

// RunOnce synchronizes all currently due connections. Individual provider
// failures are preserved in the returned jobs and combined in the returned
// error; one failed provider does not stop other providers from running.
func (scheduler *Scheduler) RunOnce(ctx context.Context) ([]SyncJob, error) {
	pendingJobs, pendingErr := scheduler.syncService.RunPending(ctx)
	records, err := scheduler.connections.ListConnections(ctx, "")
	if err != nil {
		return pendingJobs, errors.Join(pendingErr, err)
	}
	now := scheduler.clock.Now()
	due := make([]scheduledConnection, 0, len(records))
	settingsErrors := make([]error, 0)
	type cachedSettings struct {
		settings SubjectSyncSettings
		err      error
	}
	settingsBySubject := make(map[string]cachedSettings)
	for _, record := range records {
		connection := record.Connection
		if connection.Status != ConnectionActive && connection.Status != ConnectionError {
			continue
		}
		resolved, cached := settingsBySubject[connection.SubjectID]
		if !cached {
			resolved.settings, resolved.err = scheduler.subjectSyncSettings(ctx, connection.SubjectID)
			settingsBySubject[connection.SubjectID] = resolved
		}
		if resolved.err != nil {
			settingsErrors = append(settingsErrors, fmt.Errorf("subject %q: %w", connection.SubjectID, resolved.err))
			continue
		}
		if !resolved.settings.Enabled {
			continue
		}
		if connection.Status == ConnectionError {
			// An error connection is retryable only while the bounded retry
			// policy has scheduled another attempt. A nil value means that the
			// retry budget is exhausted and requires a manual sync.
			if connection.NextSyncAttemptAt == nil || connection.NextSyncAttemptAt.After(now) {
				continue
			}
			due = append(due, scheduledConnection{id: connection.ID, settings: resolved.settings})
			continue
		}
		if connection.NextSyncAttemptAt != nil && connection.NextSyncAttemptAt.After(now) {
			continue
		}
		if connection.LastSyncedAt == nil || !connection.LastSyncedAt.Add(resolved.settings.Interval).After(now) {
			due = append(due, scheduledConnection{id: connection.ID, settings: resolved.settings})
		}
	}
	sort.Slice(due, func(i, j int) bool { return due[i].id < due[j].id })
	if len(due) == 0 {
		return pendingJobs, errors.Join(append([]error{pendingErr}, settingsErrors...)...)
	}

	type outcome struct {
		job SyncJob
		err error
	}
	work := make(chan scheduledConnection)
	results := make(chan outcome, len(due))
	workers := scheduler.config.MaxConcurrent
	if workers > len(due) {
		workers = len(due)
	}
	for worker := 0; worker < workers; worker++ {
		go func() {
			for connection := range work {
				job, syncErr := scheduler.syncService.ScheduledSyncWithSettings(ctx, connection.id, connection.settings)
				results <- outcome{job: job, err: syncErr}
			}
		}()
	}
	go func() {
		defer close(work)
		for _, connection := range due {
			select {
			case work <- connection:
			case <-ctx.Done():
				return
			}
		}
	}()

	jobs := append(make([]SyncJob, 0, len(pendingJobs)+len(due)), pendingJobs...)
	errorsFound := make([]error, 0)
	if pendingErr != nil {
		errorsFound = append(errorsFound, pendingErr)
	}
	errorsFound = append(errorsFound, settingsErrors...)
	for range due {
		select {
		case result := <-results:
			jobs = append(jobs, result.job)
			if result.err != nil {
				errorsFound = append(errorsFound, result.err)
			}
		case <-ctx.Done():
			errorsFound = append(errorsFound, ctx.Err())
			return jobs, errors.Join(errorsFound...)
		}
	}
	sort.Slice(jobs, func(i, j int) bool { return jobs[i].ConnectionID < jobs[j].ConnectionID })
	return jobs, errors.Join(errorsFound...)
}

func (scheduler *Scheduler) subjectSyncSettings(ctx context.Context, subjectID string) (SubjectSyncSettings, error) {
	fallback := SubjectSyncSettings{
		Enabled: true, Interval: scheduler.config.SyncInterval, FailurePolicy: activity.FetchFailureKeepStale,
	}
	if scheduler.config.SubjectSettings == nil {
		return fallback, nil
	}
	settings, found, err := scheduler.config.SubjectSettings.LoadSubjectSyncSettings(ctx, subjectID)
	if err != nil {
		return SubjectSyncSettings{}, err
	}
	if !found {
		return fallback, nil
	}
	if settings.Interval <= 0 {
		return SubjectSyncSettings{}, ErrInvalidSyncInterval
	}
	if settings.Timezone == "" {
		return SubjectSyncSettings{}, ErrInvalidSyncTimezone
	}
	if _, err := time.LoadLocation(settings.Timezone); err != nil {
		return SubjectSyncSettings{}, fmt.Errorf("%w: %q", ErrInvalidSyncTimezone, settings.Timezone)
	}
	if settings.FailurePolicy != activity.FetchFailureKeepStale && settings.FailurePolicy != activity.FetchFailurePurge {
		return SubjectSyncSettings{}, activity.ErrInvalidFetchFailurePolicy
	}
	return settings, nil
}

// Run performs an immediate sweep, then continues until ctx is cancelled.
func (scheduler *Scheduler) Run(ctx context.Context) error {
	var runErrors []error
	if _, err := scheduler.RunOnce(ctx); err != nil {
		runErrors = append(runErrors, err)
	}
	ticker := time.NewTicker(scheduler.config.PollInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			if !errors.Is(ctx.Err(), context.Canceled) {
				runErrors = append(runErrors, ctx.Err())
			}
			return errors.Join(runErrors...)
		case <-ticker.C:
			if _, err := scheduler.RunOnce(ctx); err != nil {
				runErrors = append(runErrors, err)
			}
		}
	}
}
