package graphql

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/99designs/gqlgen/client"
	"github.com/moreal/jandibat.org/apps/api/internal/graphql/cursor"
	"github.com/moreal/jandibat.org/apps/api/internal/graphql/model"
	"github.com/moreal/jandibat.org/apps/api/internal/graphql/relayid"
	"github.com/moreal/jandibat.org/apps/api/internal/graphql/scalar"
	"github.com/moreal/jandibat.org/apps/api/internal/integrations"
	"github.com/moreal/jandibat.org/apps/api/internal/subjects"
)

const integrationSubjectID = "subject-1"
const integrationConnectionID1 = "550e8400-e29b-41d4-a716-446655440101"
const integrationConnectionID2 = "550e8400-e29b-41d4-a716-446655440102"
const integrationCustomID1 = "550e8400-e29b-41d4-a716-446655440201"
const integrationCustomID2 = "550e8400-e29b-41d4-a716-446655440202"

var integrationCreatedAt = time.Date(2026, 9, 24, 1, 2, 3, 0, time.UTC)

type integrationPagePort struct {
	connections       integrations.ConnectionPage
	custom            integrations.CustomProviderPage
	connectionErr     error
	customErr         error
	connectionSubject string
	customSubject     string
	connectionAfter   *integrations.ConnectionCursor
	customAfter       *integrations.CustomProviderCursor
	connectionFirst   int
	customFirst       int
	connectionCalls   int
	customCalls       int
}

func (port *integrationPagePort) ListPage(_ context.Context, subjectID string, after *integrations.ConnectionCursor, first int) (integrations.ConnectionPage, error) {
	port.connectionSubject, port.connectionAfter, port.connectionFirst, port.connectionCalls = subjectID, after, first, port.connectionCalls+1
	return port.connections, port.connectionErr
}

type customIntegrationPagePort struct{ *integrationPagePort }

func (port customIntegrationPagePort) ListPage(_ context.Context, subjectID string, after *integrations.CustomProviderCursor, first int) (integrations.CustomProviderPage, error) {
	port.customSubject, port.customAfter, port.customFirst, port.customCalls = subjectID, after, first, port.customCalls+1
	return port.custom, port.customErr
}

func integrationContext(owned bool, port *integrationPagePort) context.Context {
	ctx := ContextWithVerifiedViewer(context.Background(), "owner")
	ctx = ContextWithNodeServices(ctx, NodeServices{Subjects: nodeSubjectPort{subject: subjects.Subject{ID: integrationSubjectID}, owned: owned}})
	return ContextWithIntegrationQueryServices(ctx, IntegrationQueryServices{Connections: port, CustomProviders: customIntegrationPagePort{port}})
}

func TestIntegrationCatalogContainsOnlyBoundedBuiltIns(t *testing.T) {
	items, err := resolveProviderCatalog(context.Background())
	if err != nil || len(items) != 3 {
		t.Fatalf("catalog = (%#v, %v)", items, err)
	}
	seen := map[string]bool{}
	for _, item := range items {
		if item == nil || item.ID == "" || item.Name == "" || item.Kind == "custom" || item.Category != "git-hosting" {
			t.Fatalf("invalid catalog item: %#v", item)
		}
		seen[item.ID] = true
	}
	if !seen["github"] || !seen["gitlab"] || !seen["codeberg"] || len(seen) != 3 {
		t.Fatalf("unexpected catalog IDs: %#v", seen)
	}
}

func TestIntegrationConnectionsProjectSafeMetadataAndTieOrder(t *testing.T) {
	secret := "secret from storage"
	port := &integrationPagePort{connections: integrations.ConnectionPage{Connections: []integrations.ProviderConnection{
		{ID: integrationConnectionID1, SubjectID: integrationSubjectID, ProviderID: "github", EnvironmentID: "public", AuthMethod: integrations.AuthOAuth2, Status: integrations.ConnectionActive, ExternalAccountID: "account-1", ExternalAccountLogin: secret, Scopes: []string{"read:user"}, CreatedAt: integrationCreatedAt, UpdatedAt: integrationCreatedAt, LastError: secret},
		{ID: integrationConnectionID2, SubjectID: integrationSubjectID, ProviderID: "gitlab", EnvironmentID: "private", AuthMethod: integrations.AuthToken, Status: integrations.ConnectionDisabled, ExternalAccountID: "account-2", Scopes: nil, CreatedAt: integrationCreatedAt, UpdatedAt: integrationCreatedAt.Add(time.Second)},
	}, HasNextPage: true}}
	first := 2
	got, err := resolveProviderConnections(integrationContext(true, port), &model.Subject{ID: relayid.Encode(relayid.Subject, integrationSubjectID)}, &first, nil)
	if err != nil || got == nil || len(got.Edges) != 2 {
		t.Fatalf("connection page = (%#v, %v)", got, err)
	}
	if port.connectionCalls != 1 || port.connectionSubject != integrationSubjectID || port.connectionFirst != 2 || port.connectionAfter != nil {
		t.Fatalf("wrong page scope/bound: %#v", port)
	}
	if got.Edges[0].Node.ID != relayid.Encode(relayid.ProviderConnection, integrationConnectionID1) || got.Edges[0].Node.ProviderID != "github" || got.Edges[1].Node.Status != "disabled" || got.Edges[1].Node.Scopes == nil {
		t.Fatalf("wrong safe projection: %#v", got.Edges)
	}
	if got.PageInfo == nil || !got.PageInfo.HasNextPage || got.PageInfo.HasPreviousPage || got.PageInfo.StartCursor == nil || got.PageInfo.EndCursor == nil || *got.PageInfo.StartCursor != got.Edges[0].Cursor || *got.PageInfo.EndCursor != got.Edges[1].Cursor {
		t.Fatalf("wrong page info: %#v", got.PageInfo)
	}
	position, err := cursor.DecodeAs(cursor.ProviderConnection, string(got.Edges[1].Cursor))
	if err != nil || position.ID != integrationConnectionID2 || !position.Timestamp.Equal(integrationCreatedAt) {
		t.Fatalf("wrong connection cursor: (%#v, %v)", position, err)
	}
}

func TestIntegrationCustomProvidersContinueAfterDeletedAnchor(t *testing.T) {
	anchor, err := cursor.Encode(cursor.CustomProvider, cursor.Position{Timestamp: integrationCreatedAt, ID: integrationCustomID1})
	if err != nil {
		t.Fatal(err)
	}
	position := scalar.Cursor(anchor)
	port := &integrationPagePort{custom: integrations.CustomProviderPage{Providers: []integrations.CustomProvider{{ID: integrationCustomID2, SubjectID: integrationSubjectID, EnvironmentID: "private", Slug: "steps", Name: "Steps", Status: integrations.CustomProviderActive, AllowedActions: []string{"walk"}, AllowedMetrics: nil, CreatedAt: integrationCreatedAt, UpdatedAt: integrationCreatedAt}}}}
	got, err := resolveCustomProviders(integrationContext(true, port), &model.Subject{ID: relayid.Encode(relayid.Subject, integrationSubjectID)}, nil, &position)
	if err != nil || got == nil || len(got.Edges) != 1 || port.customCalls != 1 || port.customFirst != 25 || port.customSubject != integrationSubjectID || port.customAfter == nil || port.customAfter.ID != integrationCustomID1 {
		t.Fatalf("custom deleted-anchor continuation = (%#v, %v), port=%#v", got, err, port)
	}
	if !got.PageInfo.HasPreviousPage || got.PageInfo.HasNextPage || got.Edges[0].Node.ID != relayid.Encode(relayid.CustomProvider, integrationCustomID2) || got.Edges[0].Node.AllowedMetrics == nil {
		t.Fatalf("custom page metadata = %#v", got)
	}
}

func TestIntegrationPagesReturnEmptyRelayShape(t *testing.T) {
	port := &integrationPagePort{}
	parent := &model.Subject{ID: relayid.Encode(relayid.Subject, integrationSubjectID)}
	connections, err := resolveProviderConnections(integrationContext(true, port), parent, nil, nil)
	if err != nil || connections == nil || connections.Edges == nil || len(connections.Edges) != 0 || connections.PageInfo == nil || connections.PageInfo.HasNextPage || connections.PageInfo.HasPreviousPage || connections.PageInfo.StartCursor != nil || connections.PageInfo.EndCursor != nil {
		t.Fatalf("empty connections = (%#v, %v)", connections, err)
	}
	custom, err := resolveCustomProviders(integrationContext(true, port), parent, nil, nil)
	if err != nil || custom == nil || custom.Edges == nil || len(custom.Edges) != 0 || custom.PageInfo == nil || custom.PageInfo.HasNextPage || custom.PageInfo.HasPreviousPage || custom.PageInfo.StartCursor != nil || custom.PageInfo.EndCursor != nil {
		t.Fatalf("empty custom providers = (%#v, %v)", custom, err)
	}
	if port.connectionCalls != 1 || port.customCalls != 1 {
		t.Fatalf("empty pages not read: %#v", port)
	}
}

func TestIntegrationPagesDoNotReadWithoutOwnerOrValidCursor(t *testing.T) {
	wrong, err := cursor.Encode(cursor.Session, cursor.Position{Timestamp: integrationCreatedAt, ID: integrationConnectionID1})
	if err != nil {
		t.Fatal(err)
	}
	wrongCursor := scalar.Cursor(wrong)
	zero, huge := 0, 101
	parent := &model.Subject{ID: relayid.Encode(relayid.Subject, integrationSubjectID)}
	for _, tc := range []struct {
		name    string
		ctx     func(*integrationPagePort) context.Context
		parent  *model.Subject
		first   *int
		after   *scalar.Cursor
		wantErr bool
	}{
		{"anonymous", func(p *integrationPagePort) context.Context {
			return ContextWithIntegrationQueryServices(context.Background(), IntegrationQueryServices{Connections: p, CustomProviders: customIntegrationPagePort{p}})
		}, parent, nil, nil, false},
		{"nonowner", func(p *integrationPagePort) context.Context { return integrationContext(false, p) }, parent, nil, nil, false},
		{"failed auth", func(p *integrationPagePort) context.Context {
			return ContextWithIntegrationQueryServices(ContextWithFailedAuthentication(context.Background()), IntegrationQueryServices{Connections: p, CustomProviders: customIntegrationPagePort{p}})
		}, parent, nil, nil, true},
		{"wrong parent", func(p *integrationPagePort) context.Context { return integrationContext(true, p) }, &model.Subject{ID: relayid.Encode(relayid.Session, integrationSubjectID)}, nil, nil, true},
		{"zero first", func(p *integrationPagePort) context.Context { return integrationContext(true, p) }, parent, &zero, nil, true},
		{"over max", func(p *integrationPagePort) context.Context { return integrationContext(true, p) }, parent, &huge, nil, true},
		{"wrong cursor", func(p *integrationPagePort) context.Context { return integrationContext(true, p) }, parent, nil, &wrongCursor, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			port := &integrationPagePort{}
			ctx := tc.ctx(port)
			connection, connectionErr := resolveProviderConnections(ctx, tc.parent, tc.first, tc.after)
			custom, customErr := resolveCustomProviders(ctx, tc.parent, tc.first, tc.after)
			if connection != nil || custom != nil || (connectionErr != nil) != tc.wantErr || (customErr != nil) != tc.wantErr || port.connectionCalls != 0 || port.customCalls != 0 {
				t.Fatalf("rejection = (%#v,%v) (%#v,%v), port=%#v", connection, connectionErr, custom, customErr, port)
			}
		})
	}
}

func TestIntegrationPagesRejectCrossSubjectUnorderedAndPrivatePortErrors(t *testing.T) {
	parent := &model.Subject{ID: relayid.Encode(relayid.Subject, integrationSubjectID)}
	for _, tc := range []struct {
		name string
		port *integrationPagePort
	}{
		{"cross-subject", &integrationPagePort{connections: integrations.ConnectionPage{Connections: []integrations.ProviderConnection{{ID: integrationConnectionID1, SubjectID: "other", CreatedAt: integrationCreatedAt, UpdatedAt: integrationCreatedAt}}}, custom: integrations.CustomProviderPage{Providers: []integrations.CustomProvider{{ID: integrationCustomID1, SubjectID: "other", CreatedAt: integrationCreatedAt, UpdatedAt: integrationCreatedAt}}}}},
		{"out-of-order", &integrationPagePort{connections: integrations.ConnectionPage{Connections: []integrations.ProviderConnection{{ID: integrationConnectionID2, SubjectID: integrationSubjectID, CreatedAt: integrationCreatedAt, UpdatedAt: integrationCreatedAt}, {ID: integrationConnectionID1, SubjectID: integrationSubjectID, CreatedAt: integrationCreatedAt, UpdatedAt: integrationCreatedAt}}}, custom: integrations.CustomProviderPage{Providers: []integrations.CustomProvider{{ID: integrationCustomID2, SubjectID: integrationSubjectID, CreatedAt: integrationCreatedAt, UpdatedAt: integrationCreatedAt}, {ID: integrationCustomID1, SubjectID: integrationSubjectID, CreatedAt: integrationCreatedAt, UpdatedAt: integrationCreatedAt}}}}},
		{"port-error", &integrationPagePort{connectionErr: errors.New("private DB credential"), customErr: errors.New("private DB credential")}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			connection, connectionErr := resolveProviderConnections(integrationContext(true, tc.port), parent, nil, nil)
			custom, customErr := resolveCustomProviders(integrationContext(true, tc.port), parent, nil, nil)
			if connection != nil || custom != nil || !errors.Is(connectionErr, errNodeLookup) || !errors.Is(customErr, errNodeLookup) {
				t.Fatalf("fail closed = (%#v,%v),(%#v,%v)", connection, connectionErr, custom, customErr)
			}
		})
	}
}

func TestIntegrationGraphQLProjectsConnectionsAndNodesWithoutSecrets(t *testing.T) {
	connection := integrations.ProviderConnection{ID: integrationConnectionID1, SubjectID: integrationSubjectID, ProviderID: "github", EnvironmentID: "public", AuthMethod: integrations.AuthOAuth2, Status: integrations.ConnectionActive, ExternalAccountID: "account-1", ExternalAccountLogin: "private-login", Scopes: []string{"read:user"}, LastError: "secret diagnostic", CreatedAt: integrationCreatedAt, UpdatedAt: integrationCreatedAt}
	provider := integrations.CustomProvider{ID: integrationCustomID1, SubjectID: integrationSubjectID, EnvironmentID: "private", Slug: "steps", Name: "Steps", Description: "Step count", Status: integrations.CustomProviderActive, AllowedActions: []string{"walk"}, AllowedMetrics: []string{"steps"}, CreatedAt: integrationCreatedAt, UpdatedAt: integrationCreatedAt}
	port := &integrationPagePort{connections: integrations.ConnectionPage{Connections: []integrations.ProviderConnection{connection}}, custom: integrations.CustomProviderPage{Providers: []integrations.CustomProvider{provider}}}
	ctx := ContextWithVerifiedViewer(context.Background(), "owner")
	ctx = ContextWithIntegrationQueryServices(ctx, IntegrationQueryServices{Connections: port, CustomProviders: customIntegrationPagePort{port}})
	graph := viewerGraphQLClient(ctx, NodeServices{Subjects: nodeSubjectPort{subject: subjects.Subject{ID: integrationSubjectID, Handle: "mine", Timezone: "UTC", IsPublic: true, CreatedAt: integrationCreatedAt, UpdatedAt: integrationCreatedAt}, owned: true}, Connections: nodeConnectionPort{value: connection}, CustomProviders: nodeCustomPort{value: provider}})
	response, err := graph.RawPost(`query($connection: ID!, $custom: ID!) {
		providerCatalog { id kind category supportsOAuth }
		subject(handleOrID: "mine") {
			providerConnections(first: 1) { edges { node { id providerID environmentID authMethod status externalAccountID scopes privateDataEnabled createdAt updatedAt } } pageInfo { hasNextPage hasPreviousPage } }
			customProviders(first: 1) { edges { node { id environmentID slug name description status allowedActions allowedMetrics createdAt updatedAt } } }
		}
		connection: node(id: $connection) { ... on ProviderConnection { providerID environmentID authMethod status externalAccountID scopes createdAt updatedAt } }
		custom: node(id: $custom) { ... on CustomProvider { environmentID slug name description status allowedActions allowedMetrics createdAt updatedAt } }
	}`, client.Var("connection", relayid.Encode(relayid.ProviderConnection, integrationConnectionID1)), client.Var("custom", relayid.Encode(relayid.CustomProvider, integrationCustomID1)))
	if err != nil || len(response.Errors) != 0 {
		t.Fatalf("GraphQL integration projection = (%#v, %v)", response, err)
	}
	data, _ := json.Marshal(response.Data)
	for _, want := range []string{`"providerID":"github"`, `"authMethod":"oauth2"`, `"slug":"steps"`, `"allowedMetrics":["steps"]`, `"createdAt":"2026-09-24T01:02:03Z"`, `"hasPreviousPage":false`} {
		if !strings.Contains(string(data), want) {
			t.Fatalf("missing safe metadata %s: %s", want, data)
		}
	}
	for _, private := range []string{"private-login", "secret diagnostic", "EncryptedCredentials", "EncryptedIngestSecret"} {
		if strings.Contains(string(data), private) {
			t.Fatalf("private data leaked: %s", data)
		}
	}
	if port.connectionCalls != 1 || port.customCalls != 1 {
		t.Fatalf("GraphQL did not read both scoped pages: %#v", port)
	}
}

func TestIntegrationGraphQLPublicSubjectHasNullOwnerPages(t *testing.T) {
	port := &integrationPagePort{}
	ctx := ContextWithIntegrationQueryServices(context.Background(), IntegrationQueryServices{Connections: port, CustomProviders: customIntegrationPagePort{port}})
	graph := viewerGraphQLClient(ctx, NodeServices{Subjects: nodeSubjectPort{subject: subjects.Subject{ID: integrationSubjectID, Handle: "public", Timezone: "UTC", IsPublic: true, CreatedAt: integrationCreatedAt, UpdatedAt: integrationCreatedAt}, owned: false}})
	response, err := graph.RawPost(`query { subject(handleOrID: "public") { providerConnections { edges { cursor } } customProviders { edges { cursor } } } providerCatalog { id } }`)
	if err != nil || len(response.Errors) != 0 {
		t.Fatalf("public integration query = (%#v, %v)", response, err)
	}
	data, _ := json.Marshal(response.Data)
	if !strings.Contains(string(data), `"providerConnections":null`) || !strings.Contains(string(data), `"customProviders":null`) || port.connectionCalls != 0 || port.customCalls != 0 {
		t.Fatalf("public owner pages leaked: %s port=%#v", data, port)
	}
}
