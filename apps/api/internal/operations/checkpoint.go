package operations

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

var ErrInvalidMaintenanceCheckpoint = errors.New("operations: invalid maintenance checkpoint")

type MaintenanceOperation string

const (
	MaintenanceRetention            MaintenanceOperation = "retention"
	MaintenanceCredentialEncryption MaintenanceOperation = "credential_reencryption"
	MaintenanceAccountDeletion      MaintenanceOperation = "account_deletion"
)

// MaintenanceCheckpoint is deliberately opaque to persistence adapters. The
// operation owning a checkpoint validates and decodes Payload before using it.
type MaintenanceCheckpoint struct {
	Operation MaintenanceOperation
	Scope     string
	Payload   json.RawMessage
	UpdatedAt time.Time
}

type MaintenanceCheckpointStore interface {
	LoadCheckpoint(context.Context, MaintenanceOperation, string) (MaintenanceCheckpoint, bool, error)
	SaveCheckpoint(context.Context, MaintenanceCheckpoint) error
	DeleteCheckpoint(context.Context, MaintenanceOperation, string) error
}

func ValidateMaintenanceCheckpoint(checkpoint MaintenanceCheckpoint) error {
	if !validMaintenanceOperation(checkpoint.Operation) || strings.TrimSpace(checkpoint.Scope) == "" ||
		len(checkpoint.Scope) > 255 || len(checkpoint.Payload) == 0 || !json.Valid(checkpoint.Payload) || checkpoint.UpdatedAt.IsZero() {
		return fmt.Errorf("%w: operation, scope, JSON payload, and updated_at are required", ErrInvalidMaintenanceCheckpoint)
	}
	return nil
}

func validMaintenanceOperation(operation MaintenanceOperation) bool {
	switch operation {
	case MaintenanceRetention, MaintenanceCredentialEncryption, MaintenanceAccountDeletion:
		return true
	default:
		return false
	}
}
