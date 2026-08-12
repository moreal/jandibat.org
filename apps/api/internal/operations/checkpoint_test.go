package operations

import (
	"context"
	"encoding/json"
	"sync"
)

type memoryCheckpointStore struct {
	mu     sync.Mutex
	values map[string]MaintenanceCheckpoint
}

func newMemoryCheckpointStore() *memoryCheckpointStore {
	return &memoryCheckpointStore{values: make(map[string]MaintenanceCheckpoint)}
}

func checkpointKey(operation MaintenanceOperation, scope string) string {
	return string(operation) + "/" + scope
}

func (store *memoryCheckpointStore) LoadCheckpoint(_ context.Context, operation MaintenanceOperation, scope string) (MaintenanceCheckpoint, bool, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	checkpoint, ok := store.values[checkpointKey(operation, scope)]
	checkpoint.Payload = append(json.RawMessage(nil), checkpoint.Payload...)
	return checkpoint, ok, nil
}

func (store *memoryCheckpointStore) SaveCheckpoint(_ context.Context, checkpoint MaintenanceCheckpoint) error {
	store.mu.Lock()
	defer store.mu.Unlock()
	checkpoint.Payload = append(json.RawMessage(nil), checkpoint.Payload...)
	store.values[checkpointKey(checkpoint.Operation, checkpoint.Scope)] = checkpoint
	return nil
}

func (store *memoryCheckpointStore) DeleteCheckpoint(_ context.Context, operation MaintenanceOperation, scope string) error {
	store.mu.Lock()
	defer store.mu.Unlock()
	delete(store.values, checkpointKey(operation, scope))
	return nil
}
