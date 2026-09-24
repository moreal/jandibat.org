package graphql

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/moreal/jandibat.org/apps/api/internal/graphql/model"
	"github.com/moreal/jandibat.org/apps/api/internal/graphql/relayid"
	"github.com/moreal/jandibat.org/apps/api/internal/integrations"
	"github.com/moreal/jandibat.org/apps/api/internal/operations"
)

const customMutationID = "550e8400-e29b-41d4-a716-446655440404"

type customMutationPort struct {
	provider                         integrations.CustomProvider
	created                          integrations.CreateCustomProviderInput
	updated                          integrations.UpdateCustomProviderInput
	rotatedID, rotatedKey, deletedID string
	err                              error
	writeErr                         error
	calls                            []string
}

type failingKeyReader struct{}

func (failingKeyReader) Read([]byte) (int, error) { return 0, io.ErrUnexpectedEOF }

func (p *customMutationPort) Create(_ context.Context, input integrations.CreateCustomProviderInput) (integrations.CustomProvider, error) {
	p.calls = append(p.calls, "create")
	p.created = input
	return p.provider, p.err
}
func (p *customMutationPort) Get(_ context.Context, id string) (integrations.CustomProvider, error) {
	p.calls = append(p.calls, "get")
	return p.provider, p.err
}
func (p *customMutationPort) Update(_ context.Context, input integrations.UpdateCustomProviderInput) (integrations.CustomProvider, error) {
	p.calls = append(p.calls, "update")
	p.updated = input
	if p.writeErr != nil {
		return integrations.CustomProvider{}, p.writeErr
	}
	return p.provider, p.err
}
func (p *customMutationPort) RotateIngestSecret(_ context.Context, id, key string) error {
	p.calls = append(p.calls, "rotate")
	p.rotatedID, p.rotatedKey = id, key
	if p.writeErr != nil {
		return p.writeErr
	}
	return p.err
}
func (p *customMutationPort) Delete(_ context.Context, id string) error {
	p.calls = append(p.calls, "delete")
	p.deletedID = id
	if p.writeErr != nil {
		return p.writeErr
	}
	return p.err
}

func customMutationFixture() integrations.CustomProvider {
	now := time.Date(2026, 9, 24, 1, 2, 3, 0, time.UTC)
	return integrations.CustomProvider{ID: customMutationID, SubjectID: "subject-1", EnvironmentID: "custom-provider:" + customMutationID, Slug: "manual", Name: "Manual", Description: "Original", Status: integrations.CustomProviderActive, AllowedActions: []string{"commit"}, AllowedMetrics: []string{"count"}, CreatedAt: now, UpdatedAt: now}
}

func customMutationContext(p *customMutationPort, owned bool) context.Context {
	ctx := ContextWithVerifiedViewer(context.Background(), "owner")
	ctx = ContextWithNodeServices(ctx, NodeServices{Subjects: ownedMutationSubjectPort{owned: owned}})
	return ContextWithCustomProviderMutationServices(ctx, CustomProviderMutationServices{Providers: p})
}

func TestCustomProviderMutationsRequireVerifiedOwnerBeforePortCalls(t *testing.T) {
	p := &customMutationPort{provider: customMutationFixture()}
	input := model.CreateCustomProviderInput{SubjectID: relayid.Encode(relayid.Subject, "subject-1"), Slug: "manual", Name: "Manual"}
	id := relayid.Encode(relayid.CustomProvider, customMutationID)
	for _, identity := range []context.Context{context.Background(), ContextWithFailedAuthentication(context.Background())} {
		ctx := ContextWithCustomProviderMutationServices(identity, CustomProviderMutationServices{Providers: p})
		for _, run := range []func() error{
			func() error { _, err := (&mutationResolver{}).CreateCustomProvider(ctx, input); return err },
			func() error {
				_, err := (&mutationResolver{}).UpdateCustomProvider(ctx, model.UpdateCustomProviderInput{ID: id})
				return err
			},
			func() error {
				_, err := (&mutationResolver{}).RotateCustomProviderKey(ctx, model.RotateCustomProviderKeyInput{ID: id})
				return err
			},
			func() error {
				_, err := (&mutationResolver{}).DeleteCustomProvider(ctx, model.DeleteCustomProviderInput{ID: id})
				return err
			},
		} {
			if err := run(); !errors.Is(err, errNodeAuthentication) {
				t.Fatalf("unverified mutation error = %v", err)
			}
		}
	}
	if len(p.calls) != 0 {
		t.Fatalf("ports called before authentication: %v", p.calls)
	}
	ctx := customMutationContext(p, false)
	if payload, err := (&mutationResolver{}).CreateCustomProvider(ctx, input); err != nil || payload == nil || len(payload.Errors) != 1 || payload.IngestionKey != nil {
		t.Fatalf("foreign create = (%#v, %v)", payload, err)
	}
	for _, run := range []func() error{
		func() error {
			payload, err := (&mutationResolver{}).UpdateCustomProvider(ctx, model.UpdateCustomProviderInput{ID: id, Name: stringPtr("Changed")})
			if err == nil && (payload == nil || len(payload.Errors) != 1) {
				t.Fatal("foreign update accepted")
			}
			return err
		},
		func() error {
			payload, err := (&mutationResolver{}).RotateCustomProviderKey(ctx, model.RotateCustomProviderKeyInput{ID: id})
			if err == nil && (payload == nil || len(payload.Errors) != 1 || payload.IngestionKey != nil) {
				t.Fatal("foreign rotate accepted")
			}
			return err
		},
		func() error {
			payload, err := (&mutationResolver{}).DeleteCustomProvider(ctx, model.DeleteCustomProviderInput{ID: id})
			if err == nil && (payload == nil || len(payload.Errors) != 1) {
				t.Fatal("foreign delete accepted")
			}
			return err
		},
	} {
		if err := run(); err != nil {
			t.Fatalf("foreign mutation error = %v", err)
		}
	}
	if strings.Join(p.calls, ",") != "get,get,get" {
		t.Fatalf("port calls = %v", p.calls)
	}
}

func TestCreateCustomProviderGeneratesOneTimeKeyOnlyForOwnedSubject(t *testing.T) {
	p := &customMutationPort{provider: customMutationFixture()}
	ctx := customMutationContext(p, true)
	ctx = ContextWithCustomProviderMutationServices(ctx, CustomProviderMutationServices{Providers: p, KeyReader: strings.NewReader(strings.Repeat("x", 32))})
	input := model.CreateCustomProviderInput{SubjectID: relayid.Encode(relayid.Subject, "subject-1"), Slug: "manual", Name: "Manual", AllowedActions: []string{"commit"}}
	got, err := (&mutationResolver{}).CreateCustomProvider(ctx, input)
	if err != nil || got == nil || got.Provider == nil || got.IngestionKey == nil || len(got.Errors) != 0 {
		t.Fatalf("create = (%#v, %v)", got, err)
	}
	decoded, err := base64.RawURLEncoding.DecodeString(*got.IngestionKey)
	if err != nil || string(decoded) != strings.Repeat("x", 32) || p.created.IngestSecret != *got.IngestionKey || p.created.SubjectID != "subject-1" {
		t.Fatalf("generated key or scoped input incorrect: %v, %v", err, p.created.SubjectID)
	}
	if got.Provider.ID != relayid.Encode(relayid.CustomProvider, customMutationID) || got.Provider.IngestProviderID != customMutationID {
		t.Fatalf("provider identities = (%q, %q)", got.Provider.ID, got.Provider.IngestProviderID)
	}
	encoded, _ := json.Marshal(got.Provider)
	if strings.Contains(string(encoded), *got.IngestionKey) || strings.Contains(string(encoded), "secret") || strings.Contains(string(encoded), "ingestionKey") {
		t.Fatalf("provider Node exposed ingestion key")
	}
}

func TestCustomProviderKeyFailureAndPersistenceFailureNeverReturnKey(t *testing.T) {
	input := model.CreateCustomProviderInput{SubjectID: relayid.Encode(relayid.Subject, "subject-1"), Slug: "manual", Name: "Manual"}
	for _, test := range []struct {
		name      string
		reader    io.Reader
		portErr   error
		wantCalls string
	}{
		{name: "entropy", reader: failingKeyReader{}, wantCalls: ""},
		{name: "storage", reader: strings.NewReader(strings.Repeat("x", 32)), portErr: integrations.ErrConflict, wantCalls: "create"},
		{name: "internal", reader: strings.NewReader(strings.Repeat("x", 32)), portErr: errors.New("sensitive database detail"), wantCalls: "create"},
	} {
		t.Run(test.name, func(t *testing.T) {
			p := &customMutationPort{provider: customMutationFixture(), err: test.portErr}
			ctx := ContextWithCustomProviderMutationServices(customMutationContext(p, true), CustomProviderMutationServices{Providers: p, KeyReader: test.reader})
			got, err := (&mutationResolver{}).CreateCustomProvider(ctx, input)
			if (got == nil) == (err == nil) || got != nil && (got.IngestionKey != nil || got.Provider != nil) || strings.Join(p.calls, ",") != test.wantCalls {
				t.Fatalf("failed create = (%#v, %v), calls=%v", got, err, p.calls)
			}
			if err != nil && strings.Contains(err.Error(), "sensitive") {
				t.Fatal("internal error leaked")
			}
		})
	}
}

func TestUpdateCustomProviderMergesPartialFieldsAndExplicitClear(t *testing.T) {
	id := relayid.Encode(relayid.CustomProvider, customMutationID)
	for _, test := range []struct {
		name            string
		input           model.UpdateCustomProviderInput
		wantDescription string
		wantCalls       string
	}{
		{name: "omitted description", input: model.UpdateCustomProviderInput{ID: id, Name: stringPtr("Renamed")}, wantDescription: "Original", wantCalls: "get,update"},
		{name: "null description", input: model.UpdateCustomProviderInput{ID: id, Name: stringPtr("Renamed"), Description: nil}, wantDescription: "Original", wantCalls: "get,update"},
		{name: "value description", input: model.UpdateCustomProviderInput{ID: id, Description: stringPtr("New")}, wantDescription: "New", wantCalls: "get,update"},
		{name: "clear description", input: model.UpdateCustomProviderInput{ID: id, ClearDescription: boolPtr(true)}, wantDescription: "", wantCalls: "get,update"},
		{name: "conflicting clear", input: model.UpdateCustomProviderInput{ID: id, Description: stringPtr("New"), ClearDescription: boolPtr(true)}, wantDescription: "", wantCalls: "get"},
	} {
		t.Run(test.name, func(t *testing.T) {
			p := &customMutationPort{provider: customMutationFixture()}
			got, err := (&mutationResolver{}).UpdateCustomProvider(customMutationContext(p, true), test.input)
			if err != nil || got == nil || strings.Join(p.calls, ",") != test.wantCalls {
				t.Fatalf("update = (%#v, %v), calls=%v", got, err, p.calls)
			}
			if test.name == "conflicting clear" {
				if len(got.Errors) != 1 || got.Provider != nil {
					t.Fatalf("clear conflict = %#v", got)
				}
				return
			}
			if len(got.Errors) != 0 || got.Provider == nil || got.Provider.ID != id || got.Provider.IngestProviderID != customMutationID || p.updated.Description != test.wantDescription || p.updated.Status != integrations.CustomProviderActive || len(p.updated.AllowedMetrics) != 1 || p.updated.AllowedMetrics[0] != "count" {
				t.Fatalf("partial merge = %#v, %#v", got, p.updated)
			}
		})
	}
}

func TestCustomProviderWrongIDKindAndRotationDoesNotPostRead(t *testing.T) {
	p := &customMutationPort{provider: customMutationFixture()}
	ctx := ContextWithCustomProviderMutationServices(customMutationContext(p, true), CustomProviderMutationServices{Providers: p, KeyReader: strings.NewReader(strings.Repeat("x", 32))})
	wrong := relayid.Encode(relayid.Subject, customMutationID)
	if got, err := (&mutationResolver{}).DeleteCustomProvider(ctx, model.DeleteCustomProviderInput{ID: wrong}); err != nil || len(got.Errors) != 1 || len(p.calls) != 0 {
		t.Fatalf("wrong ID = (%#v, %v), calls=%v", got, err, p.calls)
	}
	id := relayid.Encode(relayid.CustomProvider, customMutationID)
	got, err := (&mutationResolver{}).RotateCustomProviderKey(ctx, model.RotateCustomProviderKeyInput{ID: id})
	if err != nil || got == nil || got.IngestionKey == nil || got.CreatedAt != nil || len(got.Errors) != 0 || strings.Join(p.calls, ",") != "get,rotate" || p.rotatedKey != *got.IngestionKey {
		t.Fatalf("rotate = (%#v, %v), calls=%v", got, err, p.calls)
	}
	p.calls = nil
	p.err = integrations.ErrNotFound
	deleted, err := (&mutationResolver{}).DeleteCustomProvider(ctx, model.DeleteCustomProviderInput{ID: id})
	if err != nil || deleted == nil || len(deleted.Errors) != 1 || deleted.DeletedProviderID != nil || strings.Join(p.calls, ",") != "get" {
		t.Fatalf("missing delete = (%#v, %v), calls=%v", deleted, err, p.calls)
	}
}

func TestCustomProviderRotationFailureAndDeleteTombstone(t *testing.T) {
	id := relayid.Encode(relayid.CustomProvider, customMutationID)
	p := &customMutationPort{provider: customMutationFixture(), err: integrations.ErrConflict}
	ctx := ContextWithCustomProviderMutationServices(customMutationContext(p, true), CustomProviderMutationServices{Providers: p, KeyReader: strings.NewReader(strings.Repeat("x", 32))})
	failed, err := (&mutationResolver{}).RotateCustomProviderKey(ctx, model.RotateCustomProviderKeyInput{ID: id})
	if err != nil || failed == nil || len(failed.Errors) != 1 || failed.IngestionKey != nil || failed.CreatedAt != nil {
		t.Fatalf("failed rotation leaked key: (%#v, %v)", failed, err)
	}
	p.err = nil
	p.calls = nil
	deleted, err := (&mutationResolver{}).DeleteCustomProvider(ctx, model.DeleteCustomProviderInput{ID: id})
	if err != nil || deleted == nil || len(deleted.Errors) != 0 || deleted.DeletedProviderID == nil || *deleted.DeletedProviderID != id || strings.Join(p.calls, ",") != "get,delete" {
		t.Fatalf("delete = (%#v, %v), calls=%v", deleted, err, p.calls)
	}
}

func TestCustomProviderUpdateRejectsInvalidStatusAndKeepsUnspecifiedActions(t *testing.T) {
	p := &customMutationPort{provider: customMutationFixture()}
	id := relayid.Encode(relayid.CustomProvider, customMutationID)
	invalid := model.CustomProviderStatus("INTERNAL")
	got, err := (&mutationResolver{}).UpdateCustomProvider(customMutationContext(p, true), model.UpdateCustomProviderInput{ID: id, Status: &invalid})
	if err != nil || got == nil || len(got.Errors) != 1 || strings.Join(p.calls, ",") != "get" {
		t.Fatalf("invalid status = (%#v, %v), calls=%v", got, err, p.calls)
	}
	p.calls = nil
	got, err = (&mutationResolver{}).UpdateCustomProvider(customMutationContext(p, true), model.UpdateCustomProviderInput{ID: id, Name: stringPtr("New")})
	if err != nil || got == nil || len(got.Errors) != 0 || len(p.updated.AllowedActions) != 1 || p.updated.AllowedActions[0] != "commit" {
		t.Fatalf("unspecified actions changed: (%#v, %v), input=%#v", got, err, p.updated)
	}
}

func TestUpdateCustomProviderRejectsNoOpBeforeAnyPortCall(t *testing.T) {
	id := relayid.Encode(relayid.CustomProvider, customMutationID)
	for _, input := range []model.UpdateCustomProviderInput{
		{ID: id},
		{ID: id, ClearDescription: boolPtr(false)},
	} {
		p := &customMutationPort{provider: customMutationFixture()}
		got, err := (&mutationResolver{}).UpdateCustomProvider(customMutationContext(p, true), input)
		if err != nil || got == nil || got.Provider != nil || len(got.Errors) != 1 || got.Errors[0].Code != "BAD_USER_INPUT" || len(p.calls) != 0 {
			t.Fatalf("no-op update = (%#v, %v), calls=%v", got, err, p.calls)
		}
	}
}

func TestCustomProviderMutationsPublishOwnedRawUUIDOnlyAfterSuccess(t *testing.T) {
	id := relayid.Encode(relayid.CustomProvider, customMutationID)
	for _, test := range []struct {
		name string
		run  func(context.Context) error
	}{
		{"create", func(ctx context.Context) error {
			payload, err := resolveCreateCustomProvider(ctx, model.CreateCustomProviderInput{SubjectID: relayid.Encode(relayid.Subject, "subject-1"), Slug: "manual", Name: "Manual"})
			if err == nil && (payload == nil || len(payload.Errors) != 0 || payload.Provider == nil || payload.IngestionKey == nil) {
				return errors.New("create did not succeed")
			}
			return err
		}},
		{"update", func(ctx context.Context) error {
			payload, err := resolveUpdateCustomProvider(ctx, model.UpdateCustomProviderInput{ID: id, Name: stringPtr("Renamed")})
			if err == nil && (payload == nil || len(payload.Errors) != 0 || payload.Provider == nil) {
				return errors.New("update did not succeed")
			}
			return err
		}},
		{"rotate", func(ctx context.Context) error {
			payload, err := resolveRotateCustomProviderKey(ctx, model.RotateCustomProviderKeyInput{ID: id})
			if err == nil && (payload == nil || len(payload.Errors) != 0 || payload.IngestionKey == nil) {
				return errors.New("rotate did not succeed")
			}
			return err
		}},
		{"delete", func(ctx context.Context) error {
			payload, err := resolveDeleteCustomProvider(ctx, model.DeleteCustomProviderInput{ID: id})
			if err == nil && (payload == nil || len(payload.Errors) != 0 || payload.DeletedProviderID == nil) {
				return errors.New("delete did not succeed")
			}
			return err
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			port := &customMutationPort{provider: customMutationFixture()}
			ctx := ContextWithCustomProviderMutationServices(customMutationContext(port, true), CustomProviderMutationServices{Providers: port, KeyReader: strings.NewReader(strings.Repeat("x", 32))})
			var targets []operations.AuditTarget
			ctx = ContextWithMutationAuditTargetPublisher(ctx, func(target operations.AuditTarget) { targets = append(targets, target) })
			if err := test.run(ctx); err != nil {
				t.Fatalf("mutation error = %v", err)
			}
			if len(targets) != 1 || targets[0] != (operations.AuditTarget{Type: "custom_provider", ID: customMutationID}) {
				t.Fatalf("canonical audit targets = %#v", targets)
			}
		})
	}
}

func TestCustomProviderMutationsNeverPublishUnverifiedOrFailedTarget(t *testing.T) {
	id := relayid.Encode(relayid.CustomProvider, customMutationID)
	for _, test := range []struct {
		name     string
		owned    bool
		err      error
		writeErr error
		run      func(context.Context) error
	}{
		{"foreign create", false, nil, nil, func(ctx context.Context) error {
			_, err := resolveCreateCustomProvider(ctx, model.CreateCustomProviderInput{SubjectID: relayid.Encode(relayid.Subject, "subject-1"), Slug: "manual", Name: "Manual"})
			return err
		}},
		{"foreign update", false, nil, nil, func(ctx context.Context) error {
			_, err := resolveUpdateCustomProvider(ctx, model.UpdateCustomProviderInput{ID: id, Name: stringPtr("Renamed")})
			return err
		}},
		{"foreign rotate", false, nil, nil, func(ctx context.Context) error {
			_, err := resolveRotateCustomProviderKey(ctx, model.RotateCustomProviderKeyInput{ID: id})
			return err
		}},
		{"foreign delete", false, nil, nil, func(ctx context.Context) error {
			_, err := resolveDeleteCustomProvider(ctx, model.DeleteCustomProviderInput{ID: id})
			return err
		}},
		{"invalid ID", true, nil, nil, func(ctx context.Context) error {
			_, err := resolveDeleteCustomProvider(ctx, model.DeleteCustomProviderInput{ID: "not-a-relay-id"})
			return err
		}},
		{"failed create", true, integrations.ErrConflict, nil, func(ctx context.Context) error {
			_, err := resolveCreateCustomProvider(ctx, model.CreateCustomProviderInput{SubjectID: relayid.Encode(relayid.Subject, "subject-1"), Slug: "manual", Name: "Manual"})
			return err
		}},
		{"failed update", true, nil, integrations.ErrConflict, func(ctx context.Context) error {
			_, err := resolveUpdateCustomProvider(ctx, model.UpdateCustomProviderInput{ID: id, Name: stringPtr("Renamed")})
			return err
		}},
		{"failed rotate", true, nil, integrations.ErrConflict, func(ctx context.Context) error {
			_, err := resolveRotateCustomProviderKey(ctx, model.RotateCustomProviderKeyInput{ID: id})
			return err
		}},
		{"failed delete", true, nil, integrations.ErrConflict, func(ctx context.Context) error {
			_, err := resolveDeleteCustomProvider(ctx, model.DeleteCustomProviderInput{ID: id})
			return err
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			port := &customMutationPort{provider: customMutationFixture(), err: test.err, writeErr: test.writeErr}
			ctx := customMutationContext(port, test.owned)
			var targets []operations.AuditTarget
			ctx = ContextWithMutationAuditTargetPublisher(ctx, func(target operations.AuditTarget) { targets = append(targets, target) })
			if err := test.run(ctx); err != nil {
				t.Fatalf("unexpected mutation error = %v", err)
			}
			if test.writeErr != nil && len(port.calls) != 2 {
				t.Fatalf("write failure did not reach owned provider write: calls=%v", port.calls)
			}
			if len(targets) != 0 {
				t.Fatalf("unverified or failed mutation published targets = %#v", targets)
			}
		})
	}
}
