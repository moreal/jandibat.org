package graphql

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/99designs/gqlgen/client"
	"github.com/99designs/gqlgen/graphql/handler"
	"github.com/moreal/jandibat.org/apps/api/internal/auth"
	"github.com/moreal/jandibat.org/apps/api/internal/graphql/generated"
	"github.com/moreal/jandibat.org/apps/api/internal/graphql/model"
	"github.com/moreal/jandibat.org/apps/api/internal/graphql/relayid"
	"github.com/moreal/jandibat.org/apps/api/internal/integrations"
	"github.com/moreal/jandibat.org/apps/api/internal/subjects"
)

func TestNodeGraphQLExecutionAndDeniedResult(t *testing.T) {
	ports := NodeServices{Subjects: nodeSubjectPort{subject: subjects.Subject{ID: "subject", IsPublic: true}, owned: false},
		Connections: nodeConnectionPort{value: integrations.ProviderConnection{ID: "connection", SubjectID: "subject"}}}
	schema := handler.NewDefaultServer(generated.NewExecutableSchema(generated.Config{Resolvers: &Resolver{}}))
	graph := client.New(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		ctx := ContextWithNodeServices(request.Context(), ports)
		schema.ServeHTTP(w, request.WithContext(ctx))
	}))
	query := `query Node($id: ID!) { node(id: $id) { __typename id } }`
	public, err := graph.RawPost(query, client.Var("id", relayid.Encode(relayid.Subject, "subject")))
	if err != nil || len(public.Errors) != 0 {
		t.Fatalf("public GraphQL node failed: response=%#v err=%v", public, err)
	}
	data, err := json.Marshal(public.Data)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), `"__typename":"Subject"`) || !strings.Contains(string(data), relayid.Encode(relayid.Subject, "subject")) {
		t.Fatalf("public GraphQL node projection: %s", data)
	}
	denied, err := graph.RawPost(query, client.Var("id", relayid.Encode(relayid.ProviderConnection, "connection")))
	if err != nil || len(denied.Errors) != 0 {
		t.Fatalf("denied GraphQL node leaked error: response=%#v err=%v", denied, err)
	}
	missing, err := graph.RawPost(query, client.Var("id", relayid.Encode(relayid.ProviderConnection, "missing")))
	if err != nil || len(missing.Errors) != 0 {
		t.Fatalf("missing GraphQL node leaked error: response=%#v err=%v", missing, err)
	}
	deniedData, _ := json.Marshal(denied.Data)
	missingData, _ := json.Marshal(missing.Data)
	if string(deniedData) != `{"node":null}` || string(missingData) != string(deniedData) {
		t.Fatalf("denied and missing must be indistinguishable: denied=%s missing=%s", deniedData, missingData)
	}
}

func TestSyncJobNodeGraphQLProjectsSafeRequiredMetadata(t *testing.T) {
	created := time.Date(2026, 9, 24, 1, 2, 3, 0, time.UTC)
	updated := created.Add(time.Minute)
	const jobID = "550e8400-e29b-41d4-a716-446655440011"
	const connectionID = "550e8400-e29b-41d4-a716-446655440012"
	ports := NodeServices{
		Subjects:    nodeSubjectPort{owned: true},
		Connections: nodeConnectionPort{value: integrations.ProviderConnection{ID: connectionID, SubjectID: "subject"}},
		SyncJobs: nodeSyncPort{value: integrations.SyncJob{
			ID: jobID, ConnectionID: connectionID, Status: integrations.SyncJobSucceeded,
			Attempt: 2, CreatedAt: created, UpdatedAt: updated,
			LastError: "sensitive error", ClaimToken: "sensitive claim",
		}},
	}
	schema := handler.NewDefaultServer(generated.NewExecutableSchema(generated.Config{Resolvers: &Resolver{}}))
	graph := client.New(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		ctx := ContextWithNodeServices(ContextWithVerifiedViewer(request.Context(), "owner"), ports)
		schema.ServeHTTP(w, request.WithContext(ctx))
	}))
	response, err := graph.RawPost(`query Node($id: ID!) { node(id: $id) { __typename id ... on SyncJob { status attempt createdAt updatedAt } } }`, client.Var("id", relayid.Encode(relayid.SyncJob, jobID)))
	if err != nil || len(response.Errors) != 0 {
		t.Fatalf("SyncJob Node query = (%#v, %v)", response, err)
	}
	data, err := json.Marshal(response.Data)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`"__typename":"SyncJob"`, `"status":"succeeded"`, `"attempt":2`, `"createdAt":"2026-09-24T01:02:03Z"`, `"updatedAt":"2026-09-24T01:03:03Z"`} {
		if !strings.Contains(string(data), want) {
			t.Fatalf("SyncJob projection missing %s: %s", want, data)
		}
	}
	if strings.Contains(string(data), "sensitive") {
		t.Fatalf("SyncJob projection leaked private metadata: %s", data)
	}
}

type nodeSubjectPort struct {
	subject  subjects.Subject
	err      error
	owned    bool
	ownerErr error
}

type nodeSubjectSpy struct {
	getActor, getID      string
	ownerActor, ownerID  string
	getCalls, ownerCalls int
}

type nodeConnectionSpy struct {
	value     integrations.ProviderConnection
	requested []string
}

func (p *nodeConnectionSpy) Get(_ context.Context, id string) (integrations.ProviderConnection, error) {
	p.requested = append(p.requested, id)
	return p.value, nil
}

type nodeCustomSpy struct {
	value     integrations.CustomProvider
	requested []string
}

func (p *nodeCustomSpy) Get(_ context.Context, id string) (integrations.CustomProvider, error) {
	p.requested = append(p.requested, id)
	return p.value, nil
}

type nodeSyncSpy struct {
	value     integrations.SyncJob
	requested []string
}

func (p *nodeSyncSpy) GetJob(_ context.Context, id string) (integrations.SyncJob, error) {
	p.requested = append(p.requested, id)
	return p.value, nil
}

type nodeSessionSpy struct {
	value            auth.Session
	actor, requested string
	calls            int
}

func (p *nodeSessionSpy) GetSessionByID(_ context.Context, actor, id string) (auth.Session, error) {
	p.actor, p.requested, p.calls = actor, id, p.calls+1
	return p.value, nil
}

func (p *nodeSubjectSpy) GetSubject(_ context.Context, actor, id string) (subjects.Subject, error) {
	p.getActor, p.getID, p.getCalls = actor, id, p.getCalls+1
	return subjects.Subject{ID: id, IsPublic: true}, nil
}

func (p *nodeSubjectSpy) OwnsSubject(_ context.Context, actor, id string) (bool, error) {
	p.ownerActor, p.ownerID, p.ownerCalls = actor, id, p.ownerCalls+1
	return true, nil
}

func TestNodeForwardsVerifiedActorAndExactParentIdentity(t *testing.T) {
	const sessionID = "8b6d3f52-e8d4-4b32-a4d0-765d3f9b6132"
	for _, tc := range []struct {
		kind               relayid.Kind
		entityID, parentID string
	}{
		{relayid.Subject, "subject-raw", ""},
		{relayid.ProviderConnection, "connection-raw", "connection-subject"},
		{relayid.CustomProvider, "custom-raw", "custom-subject"},
		{relayid.SyncJob, "job-raw", "job-subject"},
		{relayid.Session, sessionID, ""},
	} {
		t.Run(string(tc.kind), func(t *testing.T) {
			subjectSpy := &nodeSubjectSpy{}
			connection := integrations.ProviderConnection{ID: "connection-raw", SubjectID: "connection-subject"}
			if tc.kind == relayid.SyncJob {
				connection = integrations.ProviderConnection{ID: "job-connection", SubjectID: "job-subject"}
			}
			connectionSpy := &nodeConnectionSpy{value: connection}
			customSpy := &nodeCustomSpy{value: integrations.CustomProvider{ID: "custom-raw", SubjectID: "custom-subject"}}
			syncSpy := &nodeSyncSpy{value: integrations.SyncJob{ID: "job-raw", ConnectionID: "job-connection"}}
			sessionSpy := &nodeSessionSpy{value: auth.Session{ID: sessionID, UserID: "verified-owner"}}
			ctx := ContextWithNodeServices(ContextWithVerifiedViewer(context.Background(), "verified-owner"), NodeServices{
				Subjects:        subjectSpy,
				Connections:     connectionSpy,
				CustomProviders: customSpy,
				SyncJobs:        syncSpy,
				Sessions:        sessionSpy,
			})
			got, err := (&queryResolver{&Resolver{}}).Node(ctx, relayid.Encode(tc.kind, tc.entityID))
			if err != nil || got == nil {
				t.Fatalf("Node = (%#v, %v), want visible entity", got, err)
			}
			if tc.kind == relayid.Subject {
				if subjectSpy.getCalls != 1 || subjectSpy.getActor != "verified-owner" || subjectSpy.getID != tc.entityID || subjectSpy.ownerCalls != 0 {
					t.Fatalf("subject lookup forwarded wrong identity: %#v", subjectSpy)
				}
			} else if tc.kind == relayid.Session {
				if sessionSpy.calls != 1 || sessionSpy.actor != "verified-owner" || sessionSpy.requested != sessionID {
					t.Fatalf("session lookup forwarded wrong identity: %#v", sessionSpy)
				}
			} else if subjectSpy.ownerCalls != 1 || subjectSpy.ownerActor != "verified-owner" || subjectSpy.ownerID != tc.parentID || subjectSpy.getCalls != 0 {
				t.Fatalf("owner lookup forwarded wrong identity: %#v", subjectSpy)
			}
			switch tc.kind {
			case relayid.Subject, relayid.Session:
				if len(connectionSpy.requested) != 0 || len(customSpy.requested) != 0 || len(syncSpy.requested) != 0 {
					t.Fatalf("unrelated node lookup occurred: connections=%#v custom=%#v jobs=%#v", connectionSpy.requested, customSpy.requested, syncSpy.requested)
				}
			case relayid.ProviderConnection:
				if len(connectionSpy.requested) != 1 || connectionSpy.requested[0] != "connection-raw" {
					t.Fatalf("connection lookup used wrong ID: %#v", connectionSpy.requested)
				}
			case relayid.CustomProvider:
				if len(customSpy.requested) != 1 || customSpy.requested[0] != "custom-raw" {
					t.Fatalf("custom provider lookup used wrong ID: %#v", customSpy.requested)
				}
			case relayid.SyncJob:
				if len(syncSpy.requested) != 1 || syncSpy.requested[0] != "job-raw" {
					t.Fatalf("job lookup used wrong ID: %#v", syncSpy.requested)
				}
				if len(connectionSpy.requested) != 1 || connectionSpy.requested[0] != "job-connection" {
					t.Fatalf("job parent lookup used wrong ID: %#v", connectionSpy.requested)
				}
			}
		})
	}
}

func (p nodeSubjectPort) GetSubject(_ context.Context, _, _ string) (subjects.Subject, error) {
	return p.subject, p.err
}
func (p nodeSubjectPort) OwnsSubject(_ context.Context, _, _ string) (bool, error) {
	return p.owned, p.ownerErr
}

type nodeConnectionPort struct {
	value integrations.ProviderConnection
	err   error
}

func (p nodeConnectionPort) Get(context.Context, string) (integrations.ProviderConnection, error) {
	return p.value, p.err
}

type nodeCustomPort struct {
	value integrations.CustomProvider
	err   error
}

func (p nodeCustomPort) Get(context.Context, string) (integrations.CustomProvider, error) {
	return p.value, p.err
}

type nodeSyncPort struct {
	value integrations.SyncJob
	err   error
}

func (p nodeSyncPort) GetJob(context.Context, string) (integrations.SyncJob, error) {
	return p.value, p.err
}

type nodeSessionPort struct {
	value auth.Session
	err   error
}

func (p nodeSessionPort) GetSessionByID(context.Context, string, string) (auth.Session, error) {
	return p.value, p.err
}

func TestNodeResolvesFiveKindsWithGlobalIdentity(t *testing.T) {
	const sharedID = "8b6d3f52-e8d4-4b32-a4d0-765d3f9b6132"
	ports := NodeServices{
		Subjects:        nodeSubjectPort{subject: subjects.Subject{ID: sharedID, IsPublic: true}, owned: true},
		Connections:     nodeConnectionPort{value: integrations.ProviderConnection{ID: sharedID, SubjectID: "subject"}},
		CustomProviders: nodeCustomPort{value: integrations.CustomProvider{ID: sharedID, SubjectID: "subject"}},
		SyncJobs:        nodeSyncPort{value: integrations.SyncJob{ID: sharedID, ConnectionID: sharedID}},
		Sessions:        nodeSessionPort{value: auth.Session{ID: sharedID, UserID: "owner"}},
	}
	r := &queryResolver{&Resolver{}}
	ctx := ContextWithNodeServices(ContextWithVerifiedViewer(context.Background(), "owner"), ports)
	for _, tc := range []struct {
		kind relayid.Kind
		want model.Node
	}{
		{relayid.Subject, &model.Subject{ID: relayid.Encode(relayid.Subject, sharedID)}},
		{relayid.ProviderConnection, &model.ProviderConnection{ID: relayid.Encode(relayid.ProviderConnection, sharedID)}},
		{relayid.CustomProvider, &model.CustomProvider{ID: relayid.Encode(relayid.CustomProvider, sharedID)}},
		{relayid.SyncJob, &model.SyncJob{ID: relayid.Encode(relayid.SyncJob, sharedID)}},
		{relayid.Session, &model.Session{ID: relayid.Encode(relayid.Session, sharedID)}},
	} {
		t.Run(string(tc.kind), func(t *testing.T) {
			got, err := r.Node(ctx, relayid.Encode(tc.kind, sharedID))
			if err != nil || got == nil || got.GetID() != tc.want.GetID() {
				t.Fatalf("Node(%s) = (%#v, %v), want ID %q", tc.kind, got, err, tc.want.GetID())
			}
		})
	}
}

func TestNodeSubjectVisibility(t *testing.T) {
	for _, tc := range []struct {
		name, actor string
		public      bool
		lookupErr   error
		want        bool
	}{
		{"anonymous public", "", true, nil, true},
		{"anonymous private", "", false, subjects.ErrForbidden, false},
		{"owner private", "owner", false, nil, true},
		{"other private", "other", false, subjects.ErrForbidden, false},
		{"missing", "owner", false, subjects.ErrNotFound, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			if tc.actor != "" {
				ctx = ContextWithVerifiedViewer(ctx, tc.actor)
			}
			ctx = ContextWithNodeServices(ctx, NodeServices{Subjects: nodeSubjectPort{subject: subjects.Subject{ID: "subject", IsPublic: tc.public}, err: tc.lookupErr}})
			r := &queryResolver{&Resolver{}}
			got, err := r.Node(ctx, relayid.Encode(relayid.Subject, "subject"))
			if err != nil || (got != nil) != tc.want {
				t.Fatalf("Node = (%#v, %v), want visible=%t", got, err, tc.want)
			}
		})
	}
}

func TestNodePrivateEntityNeedsOwnerAndExistingParent(t *testing.T) {
	for _, tc := range []struct {
		name          string
		kind          relayid.Kind
		entityID      string
		actor         string
		owns          bool
		connectionErr error
		want          bool
	}{
		{"connection anonymous", relayid.ProviderConnection, "connection", "", true, nil, false},
		{"connection owner", relayid.ProviderConnection, "connection", "owner", true, nil, true},
		{"connection nonowner", relayid.ProviderConnection, "connection", "other", false, nil, false},
		{"connection revoked", relayid.ProviderConnection, "connection", "owner", true, integrations.ErrNotFound, false},
		{"custom anonymous", relayid.CustomProvider, "custom", "", true, nil, false},
		{"custom owner", relayid.CustomProvider, "custom", "owner", true, nil, true},
		{"custom nonowner", relayid.CustomProvider, "custom", "other", false, nil, false},
		{"job anonymous", relayid.SyncJob, "job", "", true, nil, false},
		{"job owner", relayid.SyncJob, "job", "owner", true, nil, true},
		{"job nonowner", relayid.SyncJob, "job", "other", false, nil, false},
		{"job revoked parent", relayid.SyncJob, "job", "owner", true, integrations.ErrNotFound, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			if tc.actor != "" {
				ctx = ContextWithVerifiedViewer(ctx, tc.actor)
			}
			ctx = ContextWithNodeServices(ctx, NodeServices{
				Subjects:        nodeSubjectPort{owned: tc.owns},
				Connections:     nodeConnectionPort{value: integrations.ProviderConnection{ID: "connection", SubjectID: "subject"}, err: tc.connectionErr},
				CustomProviders: nodeCustomPort{value: integrations.CustomProvider{ID: "custom", SubjectID: "subject"}},
				SyncJobs:        nodeSyncPort{value: integrations.SyncJob{ID: "job", ConnectionID: "connection"}},
			})
			r := &queryResolver{&Resolver{}}
			got, err := r.Node(ctx, relayid.Encode(tc.kind, tc.entityID))
			if err != nil || (got != nil) != tc.want {
				t.Fatalf("Node = (%#v, %v), want visible=%t", got, err, tc.want)
			}
		})
	}
}

func TestNodeSessionOwnerOnlyAndMissing(t *testing.T) {
	const sessionID = "8b6d3f52-e8d4-4b32-a4d0-765d3f9b6132"
	for _, tc := range []struct {
		name, actor string
		err         error
		want        bool
	}{
		{"anonymous", "", nil, false}, {"owner", "owner", nil, true},
		{"nonowner", "other", auth.ErrNotFound, false}, {"missing", "owner", auth.ErrNotFound, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			if tc.actor != "" {
				ctx = ContextWithVerifiedViewer(ctx, tc.actor)
			}
			ctx = ContextWithNodeServices(ctx, NodeServices{Sessions: nodeSessionPort{value: auth.Session{ID: sessionID, UserID: "owner"}, err: tc.err}})
			r := &queryResolver{&Resolver{}}
			got, err := r.Node(ctx, relayid.Encode(relayid.Session, sessionID))
			if err != nil || (got != nil) != tc.want {
				t.Fatalf("Node = (%#v, %v), want visible=%t", got, err, tc.want)
			}
		})
	}
}

func TestNodeSessionUppercaseUUIDProjectsOneCanonicalGlobalID(t *testing.T) {
	const canonical = "8b6d3f52-e8d4-4b32-a4d0-765d3f9b6132"
	upper := strings.ToUpper(canonical)
	ctx := ContextWithNodeServices(ContextWithVerifiedViewer(context.Background(), "owner"), NodeServices{
		Sessions: nodeSessionPort{value: auth.Session{ID: canonical, UserID: "owner"}},
	})
	got, err := (&queryResolver{&Resolver{}}).Node(ctx, relayid.Encode(relayid.Session, upper))
	if err != nil || got == nil || got.GetID() != relayid.Encode(relayid.Session, canonical) {
		t.Fatalf("uppercase UUID lookup = (%#v, %v), want canonical global ID", got, err)
	}
}

func TestNodeRejectsMalformedIDWithoutEchoAndRedactsInternalError(t *testing.T) {
	secret := "secret-bearer-token"
	r := &queryResolver{&Resolver{}}
	ctx := ContextWithNodeServices(context.Background(), NodeServices{Subjects: nodeSubjectPort{err: errors.New(secret)}})
	for _, id := range []string{secret, relayid.Encode(relayid.Subject, "subject")} {
		got, err := r.Node(ctx, id)
		if got != nil || err == nil || strings.Contains(err.Error(), secret) || strings.Contains(err.Error(), id) {
			t.Fatalf("Node error leaked input or backend details: got=%#v err=%v", got, err)
		}
	}
}

func TestNodeAuthenticationFailureDoesNotFallBackToAnonymousPublicRead(t *testing.T) {
	r := &queryResolver{&Resolver{}}
	ctx := ContextWithNodeServices(ContextWithFailedAuthentication(context.Background()), NodeServices{Subjects: nodeSubjectPort{subject: subjects.Subject{ID: "subject", IsPublic: true}}})
	got, err := r.Node(ctx, relayid.Encode(relayid.Subject, "subject"))
	if got != nil || err == nil || strings.Contains(err.Error(), "subject") {
		t.Fatalf("failed authentication must not read public subject: node=%#v err=%v", got, err)
	}
}

func TestNodeDoesNotProjectWrongBackendIdentity(t *testing.T) {
	r := &queryResolver{&Resolver{}}
	ctx := ContextWithNodeServices(context.Background(), NodeServices{Subjects: nodeSubjectPort{subject: subjects.Subject{ID: "other", IsPublic: true}}})
	got, err := r.Node(ctx, relayid.Encode(relayid.Subject, "subject"))
	if got != nil || err == nil {
		t.Fatalf("wrong backend identity must not resolve: node=%#v err=%v", got, err)
	}
}

func TestNodeInternalOwnerLookupErrorIsRedacted(t *testing.T) {
	r := &queryResolver{&Resolver{}}
	ctx := ContextWithNodeServices(ContextWithVerifiedViewer(context.Background(), "owner"), NodeServices{
		Subjects:    nodeSubjectPort{ownerErr: errors.New("private-database-detail")},
		Connections: nodeConnectionPort{value: integrations.ProviderConnection{ID: "connection", SubjectID: "subject"}},
	})
	got, err := r.Node(ctx, relayid.Encode(relayid.ProviderConnection, "connection"))
	if got != nil || err == nil || strings.Contains(err.Error(), "private-database-detail") {
		t.Fatalf("owner lookup detail leaked: node=%#v err=%v", got, err)
	}
}
