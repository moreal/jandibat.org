package integrations

import (
	"bytes"
	"context"
	"fmt"
	"sort"
	"sync"
	"time"
)

// MemoryStore is a concurrency-safe reference adapter for development and
// tests. Values crossing the store boundary are deeply copied.
type MemoryStore struct {
	mu                 sync.RWMutex
	connections        map[string]ConnectionRecord
	customProviders    map[string]CustomProviderRecord
	ingestedActivities map[string]map[string]IngestedActivity
	syncJobs           map[string]SyncJob
}

func NewMemoryStore() *MemoryStore {
	return &MemoryStore{
		connections:        make(map[string]ConnectionRecord),
		customProviders:    make(map[string]CustomProviderRecord),
		ingestedActivities: make(map[string]map[string]IngestedActivity),
		syncJobs:           make(map[string]SyncJob),
	}
}

func (store *MemoryStore) SaveConnection(ctx context.Context, record ConnectionRecord) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if record.Connection.ID == "" {
		return ErrEmptyConnectionID
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	if _, updating := store.connections[record.Connection.ID]; !updating {
		for _, existing := range store.connections {
			if sameConnectionIdentity(existing.Connection, record.Connection) {
				return fmt.Errorf("%w: connection already exists", ErrConflict)
			}
		}
	}
	store.connections[record.Connection.ID] = cloneConnectionRecord(record)
	return nil
}

func (store *MemoryStore) GetConnection(ctx context.Context, id string) (ConnectionRecord, error) {
	if err := ctx.Err(); err != nil {
		return ConnectionRecord{}, err
	}
	store.mu.RLock()
	defer store.mu.RUnlock()
	record, ok := store.connections[id]
	if !ok {
		return ConnectionRecord{}, fmt.Errorf("%w: connection %q", ErrNotFound, id)
	}
	return cloneConnectionRecord(record), nil
}

// ListConnections returns all connections when subjectID is empty. The empty
// form is intended for the scheduler; owner-facing services require a subject.
func (store *MemoryStore) ListConnections(ctx context.Context, subjectID string) ([]ConnectionRecord, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	store.mu.RLock()
	defer store.mu.RUnlock()
	records := make([]ConnectionRecord, 0, len(store.connections))
	for _, record := range store.connections {
		if subjectID == "" || record.Connection.SubjectID == subjectID {
			records = append(records, cloneConnectionRecord(record))
		}
	}
	sort.Slice(records, func(i, j int) bool { return records[i].Connection.ID < records[j].Connection.ID })
	return records, nil
}

func (store *MemoryStore) PurgeConnectionData(ctx context.Context, id string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	record, ok := store.connections[id]
	if !ok {
		return fmt.Errorf("%w: connection %q", ErrNotFound, id)
	}
	if record.Connection.Status != ConnectionRevoked {
		return ErrInvalidConnectionStatus
	}
	for jobID, job := range store.syncJobs {
		if job.ConnectionID == id {
			delete(store.syncJobs, jobID)
		}
	}
	return nil
}

func (store *MemoryStore) SaveCustomProvider(ctx context.Context, record CustomProviderRecord) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if record.Provider.ID == "" {
		return ErrInvalidProvider
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	if _, updating := store.customProviders[record.Provider.ID]; !updating {
		for _, existing := range store.customProviders {
			if existing.Provider.SubjectID == record.Provider.SubjectID && existing.Provider.Slug == record.Provider.Slug {
				return ErrDuplicateProviderSlug
			}
		}
	}
	store.customProviders[record.Provider.ID] = cloneCustomProviderRecord(record)
	return nil
}

func (store *MemoryStore) GetCustomProvider(ctx context.Context, id string) (CustomProviderRecord, error) {
	if err := ctx.Err(); err != nil {
		return CustomProviderRecord{}, err
	}
	store.mu.RLock()
	defer store.mu.RUnlock()
	record, ok := store.customProviders[id]
	if !ok {
		return CustomProviderRecord{}, fmt.Errorf("%w: custom provider %q", ErrNotFound, id)
	}
	return cloneCustomProviderRecord(record), nil
}

func (store *MemoryStore) ListCustomProviders(ctx context.Context, subjectID string) ([]CustomProviderRecord, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	store.mu.RLock()
	defer store.mu.RUnlock()
	records := make([]CustomProviderRecord, 0, len(store.customProviders))
	for _, record := range store.customProviders {
		if subjectID == "" || record.Provider.SubjectID == subjectID {
			records = append(records, cloneCustomProviderRecord(record))
		}
	}
	sort.Slice(records, func(i, j int) bool {
		if records[i].Provider.SubjectID != records[j].Provider.SubjectID {
			return records[i].Provider.SubjectID < records[j].Provider.SubjectID
		}
		return records[i].Provider.Slug < records[j].Provider.Slug
	})
	return records, nil
}

func (store *MemoryStore) DeleteCustomProvider(ctx context.Context, id string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	if _, ok := store.customProviders[id]; !ok {
		return fmt.Errorf("%w: custom provider %q", ErrNotFound, id)
	}
	delete(store.customProviders, id)
	delete(store.ingestedActivities, id)
	return nil
}

func (store *MemoryStore) SaveIngestedActivities(ctx context.Context, activities []IngestedActivity) ([]IngestedActivity, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	accepted := make([]IngestedActivity, 0, len(activities))
	for _, item := range activities {
		providerActivities, ok := store.ingestedActivities[item.ProviderID]
		if !ok {
			providerActivities = make(map[string]IngestedActivity)
			store.ingestedActivities[item.ProviderID] = providerActivities
		}
		if _, exists := providerActivities[item.ExternalID]; exists {
			continue
		}
		cloned := cloneIngestedActivity(item)
		providerActivities[item.ExternalID] = cloned
		accepted = append(accepted, cloneIngestedActivity(cloned))
	}
	return accepted, nil
}

func (store *MemoryStore) ListIngestedActivities(ctx context.Context, providerID string) ([]IngestedActivity, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	store.mu.RLock()
	defer store.mu.RUnlock()
	items := make([]IngestedActivity, 0, len(store.ingestedActivities[providerID]))
	for _, item := range store.ingestedActivities[providerID] {
		items = append(items, cloneIngestedActivity(item))
	}
	sort.Slice(items, func(i, j int) bool {
		if items[i].Date != items[j].Date {
			return items[i].Date < items[j].Date
		}
		return items[i].ExternalID < items[j].ExternalID
	})
	return items, nil
}

func (store *MemoryStore) SaveSyncJob(ctx context.Context, job SyncJob) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if job.ID == "" {
		return ErrNotFound
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	store.syncJobs[job.ID] = cloneSyncJob(job)
	return nil
}

func (store *MemoryStore) GetSyncJob(ctx context.Context, id string) (SyncJob, error) {
	if err := ctx.Err(); err != nil {
		return SyncJob{}, err
	}
	store.mu.RLock()
	defer store.mu.RUnlock()
	job, ok := store.syncJobs[id]
	if !ok {
		return SyncJob{}, fmt.Errorf("%w: sync job %q", ErrNotFound, id)
	}
	return cloneSyncJob(job), nil
}

func (store *MemoryStore) ListSyncJobs(ctx context.Context, connectionID string) ([]SyncJob, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	store.mu.RLock()
	defer store.mu.RUnlock()
	jobs := make([]SyncJob, 0, len(store.syncJobs))
	for _, job := range store.syncJobs {
		if connectionID == "" || job.ConnectionID == connectionID {
			jobs = append(jobs, cloneSyncJob(job))
		}
	}
	sort.Slice(jobs, func(i, j int) bool {
		if jobs[i].CreatedAt.Equal(jobs[j].CreatedAt) {
			return jobs[i].ID < jobs[j].ID
		}
		return jobs[i].CreatedAt.Before(jobs[j].CreatedAt)
	})
	return jobs, nil
}

func (store *MemoryStore) GetSyncJobByIdempotencyKey(ctx context.Context, connectionID string, keyHash []byte, now time.Time) (SyncJob, bool, error) {
	if err := ctx.Err(); err != nil {
		return SyncJob{}, false, err
	}
	store.mu.RLock()
	defer store.mu.RUnlock()
	var latest SyncJob
	found := false
	for _, job := range store.syncJobs {
		if job.ConnectionID != connectionID || !bytes.Equal(job.IdempotencyKeyHash, keyHash) ||
			job.IdempotencyExpires == nil || !job.IdempotencyExpires.After(now) {
			continue
		}
		if !found || latest.CreatedAt.Before(job.CreatedAt) {
			latest = job
			found = true
		}
	}
	if !found {
		return SyncJob{}, false, nil
	}
	return cloneSyncJob(latest), true, nil
}

func sameConnectionIdentity(left, right ProviderConnection) bool {
	return left.SubjectID == right.SubjectID &&
		left.ProviderID == right.ProviderID &&
		left.EnvironmentID == right.EnvironmentID &&
		left.ExternalAccountID == right.ExternalAccountID
}

func cloneConnectionRecord(record ConnectionRecord) ConnectionRecord {
	return ConnectionRecord{
		Connection: cloneConnection(record.Connection),
		Credentials: EncryptedCredentials{
			AccessToken:  copyBytes(record.Credentials.AccessToken),
			RefreshToken: copyBytes(record.Credentials.RefreshToken),
		},
	}
}

func cloneCustomProviderRecord(record CustomProviderRecord) CustomProviderRecord {
	return CustomProviderRecord{
		Provider:              cloneCustomProvider(record.Provider),
		EncryptedIngestSecret: copyBytes(record.EncryptedIngestSecret),
	}
}

func cloneIngestedActivity(item IngestedActivity) IngestedActivity {
	item.Metadata = copyStringMap(item.Metadata)
	item.ObservedAt = copyTimePointer(item.ObservedAt)
	return item
}
