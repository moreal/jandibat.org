package graphql

import (
	"context"

	"github.com/google/uuid"
	"github.com/moreal/jandibat.org/apps/api/internal/operations"
)

type mutationAuditTargetPublisherContextKey struct{}

// ContextWithMutationAuditTargetPublisher installs a trusted HTTP transport
// callback. Never derive it from GraphQL input, variables, aliases or headers.
func ContextWithMutationAuditTargetPublisher(ctx context.Context, publish func(operations.AuditTarget)) context.Context {
	return context.WithValue(ctx, mutationAuditTargetPublisherContextKey{}, publish)
}

// PublishMutationAuditTarget records one resolver-verified durable identity for
// the HTTP outcome. The request's mutation intent remains resource-generic.
// Invalid, encoded or noncanonical IDs cannot enter the append-only audit row.
func PublishMutationAuditTarget(ctx context.Context, targetType, rawID string) {
	target := operations.AuditTarget{Type: targetType, ID: rawID}
	if !CanonicalMutationAuditTarget(target) {
		return
	}
	publish, _ := ctx.Value(mutationAuditTargetPublisherContextKey{}).(func(operations.AuditTarget))
	if publish != nil {
		publish(target)
	}
}

// CanonicalMutationAuditTarget is the final transport guard for a resolver's
// durable target. The ID must be a raw, lowercase UUID rather than a Relay ID.
func CanonicalMutationAuditTarget(target operations.AuditTarget) bool {
	switch target.Type {
	case "account", "session", "subject", "provider_connection", "custom_provider", "sync_job":
	default:
		return false
	}
	id, err := uuid.Parse(target.ID)
	if err != nil || id == uuid.Nil || id.String() != target.ID {
		return false
	}
	return true
}
