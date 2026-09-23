package graphql

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/moreal/jandibat.org/apps/api/internal/graphql/cursor"
	"github.com/moreal/jandibat.org/apps/api/internal/graphql/model"
	"github.com/moreal/jandibat.org/apps/api/internal/graphql/relayid"
	"github.com/moreal/jandibat.org/apps/api/internal/graphql/scalar"
	"github.com/moreal/jandibat.org/apps/api/internal/integrations"
	"github.com/moreal/jandibat.org/apps/api/internal/subjects"
)

const syncConnectionID = "550e8400-e29b-41d4-a716-446655440000"
const syncJobID1 = "550e8400-e29b-41d4-a716-446655440001"
const syncJobID2 = "550e8400-e29b-41d4-a716-446655440002"

var syncCreatedAt = time.Date(2026, 9, 24, 1, 2, 3, 0, time.UTC)

type syncJobPageSpy struct {
	connectionID string
	after        *integrations.SyncJobCursor
	first        int
	calls        int
	page         integrations.SyncJobPage
	err          error
}

func (spy *syncJobPageSpy) ListJobsPage(_ context.Context, connectionID string, after *integrations.SyncJobCursor, first int) (integrations.SyncJobPage, error) {
	spy.connectionID, spy.after, spy.first = connectionID, after, first
	spy.calls++
	return spy.page, spy.err
}

func syncJobsContext(subjectPort NodeServices, pager *syncJobPageSpy) context.Context {
	ctx := ContextWithVerifiedViewer(context.Background(), "owner")
	ctx = ContextWithNodeServices(ctx, subjectPort)
	return ContextWithSyncJobPageService(ctx, pager)
}

func syncJobsPorts(owned bool, connection integrations.ProviderConnection) NodeServices {
	return NodeServices{
		Subjects:    nodeSubjectPort{subject: subjects.Subject{ID: connection.SubjectID}, owned: owned},
		Connections: nodeConnectionPort{value: connection},
	}
}

func TestSyncJobsConnectionOwnerProjectionAndTieOrder(t *testing.T) {
	connection := integrations.ProviderConnection{ID: syncConnectionID, SubjectID: "subject"}
	pageSpy := &syncJobPageSpy{page: integrations.SyncJobPage{Jobs: []integrations.SyncJob{
		{ID: syncJobID1, ConnectionID: syncConnectionID, Status: integrations.SyncJobSucceeded, Attempt: 1, CreatedAt: syncCreatedAt, UpdatedAt: syncCreatedAt, LastError: "private diagnostic", ClaimToken: "secret"},
		{ID: syncJobID2, ConnectionID: syncConnectionID, Status: integrations.SyncJobFailed, Attempt: 2, CreatedAt: syncCreatedAt, UpdatedAt: syncCreatedAt.Add(time.Second), LastError: "secret", ClaimToken: "secret"},
	}, HasNextPage: true}}
	ctx := syncJobsContext(syncJobsPorts(true, connection), pageSpy)
	first := 2
	got, err := resolveSyncJobs(ctx, &model.ProviderConnection{ID: relayid.Encode(relayid.ProviderConnection, syncConnectionID)}, &first, nil)
	if err != nil || got == nil {
		t.Fatalf("resolveSyncJobs = (%#v, %v)", got, err)
	}
	if pageSpy.calls != 1 || pageSpy.connectionID != syncConnectionID || pageSpy.after != nil || pageSpy.first != 2 {
		t.Fatalf("page port received wrong scope or bound: %#v", pageSpy)
	}
	if len(got.Edges) != 2 || got.Edges[0].Node.ID != relayid.Encode(relayid.SyncJob, syncJobID1) || got.Edges[1].Node.ID != relayid.Encode(relayid.SyncJob, syncJobID2) {
		t.Fatalf("wrong opaque identity/order: %#v", got.Edges)
	}
	if got.Edges[0].Node.Status != "succeeded" || got.Edges[1].Node.Attempt != 2 || got.Edges[1].Node.UpdatedAt != scalar.DateTime(syncCreatedAt.Add(time.Second)) {
		t.Fatalf("wrong safe metadata: %#v", got.Edges)
	}
	if got.PageInfo == nil || !got.PageInfo.HasNextPage || got.PageInfo.HasPreviousPage || got.PageInfo.StartCursor == nil || got.PageInfo.EndCursor == nil {
		t.Fatalf("wrong page info: %#v", got.PageInfo)
	}
	start, err := cursor.DecodeAs(cursor.SyncJob, string(*got.PageInfo.StartCursor))
	if err != nil || start.ID != syncJobID1 || !start.Timestamp.Equal(syncCreatedAt) {
		t.Fatalf("bad start cursor: %+v %v", start, err)
	}
	end, err := cursor.DecodeAs(cursor.SyncJob, string(*got.PageInfo.EndCursor))
	if err != nil || end.ID != syncJobID2 || got.Edges[1].Cursor != *got.PageInfo.EndCursor {
		t.Fatalf("bad end cursor: %+v %v", end, err)
	}
}

func TestSyncJobsConnectionDeletedAnchorAndDefaultPageSize(t *testing.T) {
	anchor := cursor.Position{Timestamp: syncCreatedAt, ID: syncJobID1}
	token, err := cursor.Encode(cursor.SyncJob, anchor)
	if err != nil {
		t.Fatal(err)
	}
	position := scalar.Cursor(token)
	pageSpy := &syncJobPageSpy{page: integrations.SyncJobPage{Jobs: []integrations.SyncJob{{ID: syncJobID2, ConnectionID: syncConnectionID, Status: integrations.SyncJobPending, CreatedAt: syncCreatedAt, UpdatedAt: syncCreatedAt}}}}
	connection := integrations.ProviderConnection{ID: syncConnectionID, SubjectID: "subject"}
	got, err := resolveSyncJobs(syncJobsContext(syncJobsPorts(true, connection), pageSpy), &model.ProviderConnection{ID: relayid.Encode(relayid.ProviderConnection, syncConnectionID)}, nil, &position)
	if err != nil || got == nil || pageSpy.calls != 1 || pageSpy.first != 25 || pageSpy.after == nil || pageSpy.after.ID != syncJobID1 || !pageSpy.after.CreatedAt.Equal(syncCreatedAt) {
		t.Fatalf("deleted anchor continuation = (%#v, %v), port=%#v", got, err, pageSpy)
	}
	if !got.PageInfo.HasPreviousPage || got.PageInfo.HasNextPage || len(got.Edges) != 1 {
		t.Fatalf("wrong continuation page: %#v", got)
	}
}

func TestSyncJobsConnectionRejectsUnauthorizedAndInvalidInputsBeforePageRead(t *testing.T) {
	connection := integrations.ProviderConnection{ID: syncConnectionID, SubjectID: "subject"}
	parent := &model.ProviderConnection{ID: relayid.Encode(relayid.ProviderConnection, syncConnectionID)}
	wrongCursor, err := cursor.Encode(cursor.Session, cursor.Position{Timestamp: syncCreatedAt, ID: syncJobID1})
	if err != nil {
		t.Fatal(err)
	}
	wrong := scalar.Cursor(wrongCursor)
	zero, negative, huge := 0, -1, 101
	malformed := scalar.Cursor("%%not-base64%%")
	for _, tc := range []struct {
		name   string
		ctx    func(*syncJobPageSpy) context.Context
		parent *model.ProviderConnection
		first  *int
		after  *scalar.Cursor
	}{
		{"anonymous", func(p *syncJobPageSpy) context.Context {
			return ContextWithSyncJobPageService(ContextWithNodeServices(context.Background(), syncJobsPorts(true, connection)), p)
		}, parent, nil, nil},
		{"failed auth", func(p *syncJobPageSpy) context.Context {
			return ContextWithSyncJobPageService(ContextWithNodeServices(ContextWithFailedAuthentication(context.Background()), syncJobsPorts(true, connection)), p)
		}, parent, nil, nil},
		{"wrong parent kind", func(p *syncJobPageSpy) context.Context { return syncJobsContext(syncJobsPorts(true, connection), p) }, &model.ProviderConnection{ID: relayid.Encode(relayid.Subject, syncConnectionID)}, nil, nil},
		{"zero first", func(p *syncJobPageSpy) context.Context { return syncJobsContext(syncJobsPorts(true, connection), p) }, parent, &zero, nil},
		{"negative first", func(p *syncJobPageSpy) context.Context { return syncJobsContext(syncJobsPorts(true, connection), p) }, parent, &negative, nil},
		{"over max", func(p *syncJobPageSpy) context.Context { return syncJobsContext(syncJobsPorts(true, connection), p) }, parent, &huge, nil},
		{"wrong cursor kind", func(p *syncJobPageSpy) context.Context { return syncJobsContext(syncJobsPorts(true, connection), p) }, parent, nil, &wrong},
		{"malformed cursor", func(p *syncJobPageSpy) context.Context { return syncJobsContext(syncJobsPorts(true, connection), p) }, parent, nil, &malformed},
	} {
		t.Run(tc.name, func(t *testing.T) {
			pageSpy := &syncJobPageSpy{}
			got, err := resolveSyncJobs(tc.ctx(pageSpy), tc.parent, tc.first, tc.after)
			if err == nil || got != nil || pageSpy.calls != 0 {
				t.Fatalf("resolveSyncJobs = (%#v, %v), page calls=%d", got, err, pageSpy.calls)
			}
		})
	}
}

func TestSyncJobsConnectionMissingRevokedOrDeniedParentIsNull(t *testing.T) {
	connection := integrations.ProviderConnection{ID: syncConnectionID, SubjectID: "subject"}
	parent := &model.ProviderConnection{ID: relayid.Encode(relayid.ProviderConnection, syncConnectionID)}
	for _, tc := range []struct {
		name  string
		ports NodeServices
	}{
		{"missing", NodeServices{Subjects: nodeSubjectPort{owned: true}, Connections: nodeConnectionPort{err: integrations.ErrNotFound}}},
		{"nonowner", syncJobsPorts(false, connection)},
		{"revoked", func() NodeServices {
			c := connection
			c.Status = integrations.ConnectionRevoked
			return syncJobsPorts(true, c)
		}()},
		{"parent mismatch", func() NodeServices { c := connection; c.ID = "other"; return syncJobsPorts(true, c) }()},
	} {
		t.Run(tc.name, func(t *testing.T) {
			pageSpy := &syncJobPageSpy{}
			got, err := resolveSyncJobs(syncJobsContext(tc.ports, pageSpy), parent, nil, nil)
			if got != nil || err != nil || pageSpy.calls != 0 {
				t.Fatalf("denied/missing parent = (%#v, %v), page calls=%d", got, err, pageSpy.calls)
			}
		})
	}
}

func TestSyncJobsConnectionInternalParentFailureIsRedactedError(t *testing.T) {
	ports := NodeServices{Subjects: nodeSubjectPort{owned: true}, Connections: nodeConnectionPort{err: errors.New("private database details")}}
	pageSpy := &syncJobPageSpy{}
	got, err := resolveSyncJobs(syncJobsContext(ports, pageSpy), &model.ProviderConnection{ID: relayid.Encode(relayid.ProviderConnection, syncConnectionID)}, nil, nil)
	if got != nil || !errors.Is(err, errNodeLookup) || pageSpy.calls != 0 {
		t.Fatalf("internal failure = (%#v, %v), calls=%d", got, err, pageSpy.calls)
	}
}

func TestSyncJobsConnectionRejectsCrossParentPageAndPortError(t *testing.T) {
	connection := integrations.ProviderConnection{ID: syncConnectionID, SubjectID: "subject"}
	parent := &model.ProviderConnection{ID: relayid.Encode(relayid.ProviderConnection, syncConnectionID)}
	for _, tc := range []struct {
		name string
		page integrations.SyncJobPage
		err  error
	}{
		{"cross-parent", integrations.SyncJobPage{Jobs: []integrations.SyncJob{{ID: syncJobID1, ConnectionID: "other", CreatedAt: syncCreatedAt}}}, nil},
		{"out-of-order", integrations.SyncJobPage{Jobs: []integrations.SyncJob{
			{ID: syncJobID2, ConnectionID: syncConnectionID, CreatedAt: syncCreatedAt, UpdatedAt: syncCreatedAt},
			{ID: syncJobID1, ConnectionID: syncConnectionID, CreatedAt: syncCreatedAt, UpdatedAt: syncCreatedAt},
		}}, nil},
		{"invalid-id", integrations.SyncJobPage{Jobs: []integrations.SyncJob{{ID: "private token", ConnectionID: syncConnectionID, CreatedAt: syncCreatedAt, UpdatedAt: syncCreatedAt}}}, nil},
		{"port error", integrations.SyncJobPage{}, errors.New("private database credential")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			pageSpy := &syncJobPageSpy{page: tc.page, err: tc.err}
			got, err := resolveSyncJobs(syncJobsContext(syncJobsPorts(true, connection), pageSpy), parent, nil, nil)
			if got != nil || !errors.Is(err, errNodeLookup) || pageSpy.calls != 1 {
				t.Fatalf("resolveSyncJobs = (%#v, %v), calls=%d", got, err, pageSpy.calls)
			}
		})
	}
}

func TestSyncJobsConnectionForwardsVerifiedActorAndRawParent(t *testing.T) {
	subjectSpy := &nodeSubjectSpy{}
	connectionSpy := &nodeConnectionSpy{value: integrations.ProviderConnection{ID: syncConnectionID, SubjectID: "subject-raw"}}
	pageSpy := &syncJobPageSpy{}
	ctx := ContextWithVerifiedViewer(context.Background(), "verified-owner")
	ctx = ContextWithNodeServices(ctx, NodeServices{Subjects: subjectSpy, Connections: connectionSpy})
	ctx = ContextWithSyncJobPageService(ctx, pageSpy)
	got, err := resolveSyncJobs(ctx, &model.ProviderConnection{ID: relayid.Encode(relayid.ProviderConnection, syncConnectionID)}, nil, nil)
	if err != nil || got == nil || len(got.Edges) != 0 || got.PageInfo == nil {
		t.Fatalf("resolveSyncJobs = (%#v, %v)", got, err)
	}
	if len(connectionSpy.requested) != 1 || connectionSpy.requested[0] != syncConnectionID || subjectSpy.ownerActor != "verified-owner" || subjectSpy.ownerID != "subject-raw" || subjectSpy.ownerCalls != 1 || subjectSpy.getCalls != 0 || pageSpy.connectionID != syncConnectionID {
		t.Fatalf("wrong identity forwarding: connection=%#v subject=%#v page=%#v", connectionSpy, subjectSpy, pageSpy)
	}
	if got.PageInfo.StartCursor != nil || got.PageInfo.EndCursor != nil || got.PageInfo.HasPreviousPage || got.PageInfo.HasNextPage {
		t.Fatalf("empty connection has cursor or pagination flags: %#v", got.PageInfo)
	}
}
