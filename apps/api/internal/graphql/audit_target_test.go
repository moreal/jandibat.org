package graphql

import (
	"context"
	"testing"

	"github.com/moreal/jandibat.org/apps/api/internal/operations"
)

func TestPublishMutationAuditTargetOnlyAcceptsCanonicalDurableIdentity(t *testing.T) {
	var published []operations.AuditTarget
	ctx := ContextWithMutationAuditTargetPublisher(context.Background(), func(target operations.AuditTarget) {
		published = append(published, target)
	})
	const rawID = "8d28dfdb-a447-48fd-953d-c94d34719e29"
	for _, test := range []struct{ targetType, id string }{
		{"subject", rawID},
		{"provider_connection", rawID},
		{"custom_provider", rawID},
		{"sync_job", rawID},
		{"session", rawID},
		{"account", rawID},
	} {
		PublishMutationAuditTarget(ctx, test.targetType, test.id)
	}
	if len(published) != 6 {
		t.Fatalf("published targets = %#v", published)
	}
	for _, target := range published {
		if target.ID != rawID {
			t.Fatalf("noncanonical target = %#v", target)
		}
	}
	for _, test := range []struct{ targetType, id string }{
		{"graphql", rawID},
		{"subject", ""},
		{"subject", "  "},
		{"subject", "00000000-0000-0000-0000-000000000000"},
		{"subject", "c3ViamVjdDo4ZDI4ZGZkYi1hNDQ3LTQ4ZmQtOTUzZC1jOTRkMzQ3MTllMjk="},
		{"subject", "8D28DFDB-A447-48FD-953D-C94D34719E29"},
		{"subject", "8d28dfdb-a447-48fd-953d-c94d34719e29?token=secret"},
	} {
		PublishMutationAuditTarget(ctx, test.targetType, test.id)
	}
	if len(published) != 6 {
		t.Fatalf("invalid target published: %#v", published)
	}
	PublishMutationAuditTarget(context.Background(), "subject", rawID)
}
