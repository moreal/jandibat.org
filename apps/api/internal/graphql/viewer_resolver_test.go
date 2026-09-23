package graphql

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/99designs/gqlgen/client"
	"github.com/99designs/gqlgen/graphql/handler"
	"github.com/moreal/jandibat.org/apps/api/internal/auth"
	"github.com/moreal/jandibat.org/apps/api/internal/graphql/cursor"
	"github.com/moreal/jandibat.org/apps/api/internal/graphql/generated"
	"github.com/moreal/jandibat.org/apps/api/internal/graphql/relayid"
	"github.com/moreal/jandibat.org/apps/api/internal/graphql/scalar"
	"github.com/moreal/jandibat.org/apps/api/internal/subjects"
)

type viewerUserPort struct {
	value subjects.User
	err   error
	actor string
	calls int
}

func (port *viewerUserPort) GetCurrentUser(_ context.Context, actor string) (subjects.User, error) {
	port.actor, port.calls = actor, port.calls+1
	return port.value, port.err
}

type viewerSessionPagePort struct {
	items []auth.Session
	err   error
	actor string
	after *auth.SessionCursor
	first int
	calls int
}

func (port *viewerSessionPagePort) ListSessionsPage(_ context.Context, actor string, after *auth.SessionCursor, first int) ([]auth.Session, error) {
	port.actor, port.after, port.first, port.calls = actor, after, first, port.calls+1
	return port.items, port.err
}

func viewerGraphQLClient(ctx context.Context, services NodeServices) *client.Client {
	server := handler.NewDefaultServer(generated.NewExecutableSchema(generated.Config{Resolvers: &Resolver{}}))
	return client.New(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		request = request.WithContext(ContextWithNodeServices(ctx, services))
		server.ServeHTTP(w, request)
	}))
}

func TestViewerProjectsOnlyVerifiedUserProfile(t *testing.T) {
	created := time.Date(2024, 9, 1, 1, 2, 3, 0, time.UTC)
	updated := created.Add(time.Hour)
	emailVerified := created.Add(time.Minute)
	users := &viewerUserPort{value: subjects.User{ID: "account-1", PrimaryEmail: "owner@example.test", Status: subjects.UserStatusActive, EmailVerifiedAt: &emailVerified, CreatedAt: created, UpdatedAt: updated}}
	graph := viewerGraphQLClient(ContextWithVerifiedViewer(context.Background(), "account-1"), NodeServices{ViewerUsers: users})
	response, err := graph.RawPost(`query { viewer { user { id primaryEmail status emailVerifiedAt createdAt updatedAt } } }`)
	if err != nil || len(response.Errors) != 0 {
		t.Fatalf("viewer profile query = (%#v, %v)", response, err)
	}
	data, _ := json.Marshal(response.Data)
	want := `{"viewer":{"user":{"createdAt":"2024-09-01T01:02:03Z","emailVerifiedAt":"2024-09-01T01:03:03Z","id":"account-1","primaryEmail":"owner@example.test","status":"active","updatedAt":"2024-09-01T02:02:03Z"}}}`
	if string(data) != want || users.calls != 1 || users.actor != "account-1" {
		t.Fatalf("viewer profile = %s, lookup=%#v; want %s", data, users, want)
	}
}

func TestViewerAnonymousAndFailedAuthenticationStayDistinct(t *testing.T) {
	users := &viewerUserPort{value: subjects.User{ID: "account-1", Status: subjects.UserStatusActive}}
	for _, tc := range []struct {
		name      string
		ctx       context.Context
		wantError bool
	}{
		{"anonymous", context.Background(), false},
		{"failed authentication", ContextWithFailedAuthentication(context.Background()), true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			response, err := viewerGraphQLClient(tc.ctx, NodeServices{ViewerUsers: users}).RawPost(`query { viewer { user { id } } }`)
			if err != nil {
				t.Fatal(err)
			}
			if tc.wantError {
				if len(response.Errors) == 0 {
					t.Fatalf("errors = %#v", response.Errors)
				}
			} else {
				data, _ := json.Marshal(response.Data)
				if len(response.Errors) != 0 || string(data) != `{"viewer":null}` {
					t.Fatalf("response = %#v data=%s", response, data)
				}
			}
		})
	}
	if users.calls != 0 {
		t.Fatalf("unauthenticated lookup calls = %d", users.calls)
	}
}

func TestViewerUnavailableUserDoesNotExposeInternalError(t *testing.T) {
	secret := "private-database-detail"
	users := &viewerUserPort{err: errors.New(secret)}
	response, err := viewerGraphQLClient(ContextWithVerifiedViewer(context.Background(), "account-1"), NodeServices{ViewerUsers: users}).RawPost(`query { viewer { user { id } } }`)
	if err != nil || len(response.Errors) == 0 || strings.Contains(string(response.Errors), secret) {
		t.Fatalf("internal failure leaked: response=%#v err=%v", response, err)
	}
}

func TestViewerSessionsProjectsOwnerSafeMetadataAndLookahead(t *testing.T) {
	const one = "00000000-0000-4000-8000-000000000001"
	const two = "00000000-0000-4000-8000-000000000002"
	const three = "00000000-0000-4000-8000-000000000003"
	now := time.Date(2024, 9, 1, 1, 2, 3, 0, time.UTC)
	revoked := now.Add(time.Minute)
	lastSeen := now.Add(2 * time.Minute)
	users := &viewerUserPort{value: subjects.User{ID: "owner", Status: subjects.UserStatusActive, CreatedAt: now, UpdatedAt: now}}
	pages := &viewerSessionPagePort{items: []auth.Session{
		{ID: one, UserID: "owner", CreatedAt: now, ExpiresAt: now.Add(time.Hour), IPAddress: "127.0.0.1", UserAgent: "test agent", RevokedAt: &revoked, LastSeenAt: &lastSeen, TokenHash: auth.Digest{1}},
		{ID: two, UserID: "owner", CreatedAt: now, ExpiresAt: now.Add(time.Hour)},
		{ID: three, UserID: "owner", CreatedAt: now.Add(-time.Hour), ExpiresAt: now.Add(time.Hour)},
	}}
	graph := viewerGraphQLClient(ContextWithVerifiedViewer(context.Background(), "owner"), NodeServices{ViewerUsers: users, SessionPages: pages})
	response, err := graph.RawPost(`query { viewer { sessions(first: 2) { edges { cursor node { id createdAt expiresAt revokedAt lastSeenAt ipAddress userAgent } } pageInfo { hasNextPage hasPreviousPage startCursor endCursor } } } }`)
	if err != nil || len(response.Errors) != 0 {
		t.Fatalf("session page = (%#v, %v)", response, err)
	}
	data, _ := json.Marshal(response.Data)
	if strings.Contains(string(data), "000000000003") || strings.Contains(string(data), "TokenHash") || !strings.Contains(string(data), relayid.Encode(relayid.Session, one)) || !strings.Contains(string(data), relayid.Encode(relayid.Session, two)) || !strings.Contains(string(data), `"hasNextPage":true`) || !strings.Contains(string(data), `"hasPreviousPage":false`) || !strings.Contains(string(data), `"ipAddress":"127.0.0.1"`) || !strings.Contains(string(data), `"revokedAt":"2024-09-01T01:03:03Z"`) {
		t.Fatalf("unsafe/incomplete session page: %s", data)
	}
	if pages.calls != 1 || pages.actor != "owner" || pages.after != nil || pages.first != 2 {
		t.Fatalf("page port forwarded wrong request: %#v", pages)
	}
	var result struct {
		Viewer struct {
			Sessions struct {
				Edges []struct {
					Cursor string `json:"cursor"`
				}
				PageInfo struct {
					StartCursor string `json:"startCursor"`
					EndCursor   string `json:"endCursor"`
				} `json:"pageInfo"`
			} `json:"sessions"`
		} `json:"viewer"`
	}
	if err := json.Unmarshal(data, &result); err != nil {
		t.Fatal(err)
	}
	if len(result.Viewer.Sessions.Edges) != 2 || result.Viewer.Sessions.PageInfo.StartCursor != result.Viewer.Sessions.Edges[0].Cursor || result.Viewer.Sessions.PageInfo.EndCursor != result.Viewer.Sessions.Edges[1].Cursor {
		t.Fatalf("cursor boundaries: %s", data)
	}
	for i, id := range []string{one, two} {
		position, err := cursor.DecodeAs(cursor.Session, result.Viewer.Sessions.Edges[i].Cursor)
		if err != nil || position.ID != id || !position.Timestamp.Equal(now) {
			t.Fatalf("edge %d position = (%#v, %v)", i, position, err)
		}
	}
}

func TestViewerSessionsForwardsDeletedEqualTimestampAnchor(t *testing.T) {
	const anchorID = "00000000-0000-4000-8000-000000000002"
	const nextID = "00000000-0000-4000-8000-000000000003"
	now := time.Date(2024, 9, 1, 1, 2, 3, 0, time.UTC)
	anchor, err := cursor.Encode(cursor.Session, cursor.Position{Timestamp: now, ID: anchorID})
	if err != nil {
		t.Fatal(err)
	}
	users := &viewerUserPort{value: subjects.User{ID: "owner", Status: subjects.UserStatusActive, CreatedAt: now, UpdatedAt: now}}
	pages := &viewerSessionPagePort{items: []auth.Session{{ID: nextID, UserID: "owner", CreatedAt: now, ExpiresAt: now.Add(time.Hour)}}}
	graph := viewerGraphQLClient(ContextWithVerifiedViewer(context.Background(), "owner"), NodeServices{ViewerUsers: users, SessionPages: pages})
	response, err := graph.RawPost(`query($after: Cursor) { viewer { sessions(first: 2, after: $after) { edges { node { id } } pageInfo { hasNextPage hasPreviousPage } } } }`, client.Var("after", anchor))
	if err != nil || len(response.Errors) != 0 {
		t.Fatalf("after deleted anchor = (%#v, %v)", response, err)
	}
	data, _ := json.Marshal(response.Data)
	if pages.calls != 1 || pages.after == nil || pages.after.ID != anchorID || !pages.after.CreatedAt.Equal(now) || !strings.Contains(string(data), relayid.Encode(relayid.Session, nextID)) || !strings.Contains(string(data), `"hasPreviousPage":true`) || !strings.Contains(string(data), `"hasNextPage":false`) {
		t.Fatalf("deleted anchor page = %s; port=%#v", data, pages)
	}
}

func TestViewerSessionsRejectsInvalidArgumentsWithoutPortCall(t *testing.T) {
	now := time.Date(2024, 9, 1, 1, 2, 3, 0, time.UTC)
	wrong, err := cursor.Encode(cursor.Subject, cursor.Position{Timestamp: now, ID: "subject"})
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name  string
		first *int
		after *scalar.Cursor
	}{
		{"zero", intPointer(0), nil}, {"negative", intPointer(-1), nil}, {"too many", intPointer(101), nil},
		{"malformed cursor", nil, cursorPointer("not-a-cursor")}, {"wrong connection", nil, cursorPointer(wrong)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			users := &viewerUserPort{value: subjects.User{ID: "owner", Status: subjects.UserStatusActive}}
			pages := &viewerSessionPagePort{}
			ctx := ContextWithNodeServices(ContextWithVerifiedViewer(context.Background(), "owner"), NodeServices{ViewerUsers: users, SessionPages: pages})
			viewer, err := (&queryResolver{&Resolver{}}).Viewer(ctx)
			if err != nil {
				t.Fatal(err)
			}
			got, err := (&viewerResolver{&Resolver{}}).Sessions(ctx, viewer, tc.first, tc.after)
			if got != nil || err == nil || pages.calls != 0 {
				t.Fatalf("invalid args = (%#v, %v), page calls=%d", got, err, pages.calls)
			}
		})
	}
}

func TestViewerSessionsRejectsWrongOwnerRowsAndPreservesEmptyPage(t *testing.T) {
	const sessionID = "00000000-0000-4000-8000-000000000001"
	now := time.Date(2024, 9, 1, 1, 2, 3, 0, time.UTC)
	users := &viewerUserPort{value: subjects.User{ID: "owner", Status: subjects.UserStatusActive}}
	pages := &viewerSessionPagePort{}
	ctx := ContextWithNodeServices(ContextWithVerifiedViewer(context.Background(), "owner"), NodeServices{ViewerUsers: users, SessionPages: pages})
	viewer, err := (&queryResolver{&Resolver{}}).Viewer(ctx)
	if err != nil {
		t.Fatal(err)
	}
	page, err := (&viewerResolver{&Resolver{}}).Sessions(ctx, viewer, nil, nil)
	if err != nil || page == nil || len(page.Edges) != 0 || page.PageInfo == nil || page.PageInfo.HasNextPage || page.PageInfo.HasPreviousPage || page.PageInfo.StartCursor != nil || page.PageInfo.EndCursor != nil || pages.first != 25 {
		t.Fatalf("empty/default page=(%#v,%v), port=%#v", page, err, pages)
	}
	pages.items = []auth.Session{{ID: sessionID, UserID: "other", CreatedAt: now}}
	page, err = (&viewerResolver{&Resolver{}}).Sessions(ctx, viewer, nil, nil)
	if page != nil || err == nil {
		t.Fatalf("cross-owner page = (%#v,%v)", page, err)
	}
}

func TestViewerSessionsInvalidCursorHasStableUserErrorExtension(t *testing.T) {
	users := &viewerUserPort{value: subjects.User{ID: "owner", Status: subjects.UserStatusActive}}
	pages := &viewerSessionPagePort{}
	graph := viewerGraphQLClient(ContextWithVerifiedViewer(context.Background(), "owner"), NodeServices{ViewerUsers: users, SessionPages: pages})
	response, err := graph.RawPost(`query { viewer { sessions(after: "invalid") { edges { cursor } } } }`)
	if err != nil || !strings.Contains(string(response.Errors), `"code":"BAD_USER_INPUT"`) || pages.calls != 0 {
		t.Fatalf("bad cursor response = (%#v,%v); calls=%d", response, err, pages.calls)
	}
}

func TestSessionNodeProjectsSafeOwnerMetadata(t *testing.T) {
	const sessionID = "00000000-0000-4000-8000-000000000001"
	now := time.Date(2024, 9, 1, 1, 2, 3, 0, time.UTC)
	session := &nodeSessionSpy{value: auth.Session{ID: sessionID, UserID: "owner", CreatedAt: now, ExpiresAt: now.Add(time.Hour), IPAddress: "127.0.0.1", UserAgent: "client", TokenHash: auth.Digest{1}}}
	graph := viewerGraphQLClient(ContextWithVerifiedViewer(context.Background(), "owner"), NodeServices{Sessions: session})
	response, err := graph.RawPost(`query($id: ID!) { node(id: $id) { ... on Session { id createdAt expiresAt ipAddress userAgent } } }`, client.Var("id", relayid.Encode(relayid.Session, sessionID)))
	if err != nil || len(response.Errors) != 0 {
		t.Fatalf("session node = (%#v,%v)", response, err)
	}
	data, _ := json.Marshal(response.Data)
	if !strings.Contains(string(data), `"createdAt":"2024-09-01T01:02:03Z"`) || !strings.Contains(string(data), `"expiresAt":"2024-09-01T02:02:03Z"`) || !strings.Contains(string(data), `"ipAddress":"127.0.0.1"`) || !strings.Contains(string(data), `"userAgent":"client"`) || strings.Contains(string(data), "TokenHash") {
		t.Fatalf("Node metadata projection=%s", data)
	}
	if session.actor != "owner" || session.requested != sessionID {
		t.Fatalf("Node port request = %#v", session)
	}
}

func TestViewerUnavailableIdentityNeverQueriesSessions(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  error
	}{
		{"disabled user", subjects.ErrForbidden},
		{"missing user", subjects.ErrUnauthenticated},
	} {
		t.Run(tc.name, func(t *testing.T) {
			users := &viewerUserPort{err: tc.err}
			pages := &viewerSessionPagePort{}
			response, err := viewerGraphQLClient(ContextWithVerifiedViewer(context.Background(), "owner"), NodeServices{ViewerUsers: users, SessionPages: pages}).RawPost(`query { viewer { sessions { edges { cursor } } } }`)
			if err != nil || len(response.Errors) == 0 || pages.calls != 0 {
				t.Fatalf("unavailable viewer = (%#v,%v), session calls=%d", response, err, pages.calls)
			}
		})
	}
}

type viewerNoopMailer struct{}

func (viewerNoopMailer) SendMagicLink(context.Context, auth.MagicLinkMail) error { return nil }

func TestViewerSessionConnectionTraversesEqualTimestampsWithoutDuplicateOrOtherOwner(t *testing.T) {
	const owner = "owner"
	const other = "other"
	now := time.Date(2024, 9, 1, 1, 2, 3, 0, time.UTC)
	store := auth.NewMemoryStore()
	for _, userID := range []string{owner, other} {
		if err := store.SaveUser(context.Background(), auth.User{ID: userID, PrimaryEmail: userID + "@example.test", Status: auth.UserStatusActive, CreatedAt: now, UpdatedAt: now}); err != nil {
			t.Fatal(err)
		}
	}
	service, err := auth.NewService(auth.Dependencies{Repository: store, Mailer: viewerNoopMailer{}}, auth.Config{Now: func() time.Time { return now }})
	if err != nil {
		t.Fatal(err)
	}
	ids := []string{
		"00000000-0000-4000-8000-000000000003",
		"00000000-0000-4000-8000-000000000001",
		"00000000-0000-4000-8000-000000000002",
		"00000000-0000-4000-8000-000000000004",
	}
	for i, id := range ids {
		created := now
		if i == 3 {
			created = now.Add(-time.Hour)
		}
		if err := store.SaveSession(context.Background(), auth.Session{ID: id, UserID: owner, TokenHash: auth.Digest(sha256.Sum256([]byte(id))), CreatedAt: created, ExpiresAt: now.Add(time.Hour)}); err != nil {
			t.Fatal(err)
		}
	}
	if err := store.SaveSession(context.Background(), auth.Session{ID: "00000000-0000-4000-8000-000000000005", UserID: other, TokenHash: auth.Digest(sha256.Sum256([]byte(other))), CreatedAt: now, ExpiresAt: now.Add(time.Hour)}); err != nil {
		t.Fatal(err)
	}
	users := &viewerUserPort{value: subjects.User{ID: owner, Status: subjects.UserStatusActive, CreatedAt: now, UpdatedAt: now}}
	graph := viewerGraphQLClient(ContextWithVerifiedViewer(context.Background(), owner), NodeServices{ViewerUsers: users, SessionPages: service})
	var after any
	var seen []string
	for pageNumber := 0; pageNumber < 2; pageNumber++ {
		response, err := graph.RawPost(`query($after: Cursor) { viewer { sessions(first: 2, after: $after) { edges { cursor node { id } } pageInfo { hasNextPage } } } }`, client.Var("after", after))
		if err != nil || len(response.Errors) != 0 {
			t.Fatalf("page %d = (%#v,%v)", pageNumber, response, err)
		}
		data, _ := json.Marshal(response.Data)
		var result struct {
			Viewer struct {
				Sessions struct {
					Edges []struct {
						Cursor string `json:"cursor"`
						Node   struct {
							ID string `json:"id"`
						} `json:"node"`
					} `json:"edges"`
					PageInfo struct {
						HasNextPage bool `json:"hasNextPage"`
					} `json:"pageInfo"`
				} `json:"sessions"`
			} `json:"viewer"`
		}
		if err := json.Unmarshal(data, &result); err != nil {
			t.Fatal(err)
		}
		if len(result.Viewer.Sessions.Edges) != 2 || result.Viewer.Sessions.PageInfo.HasNextPage != (pageNumber == 0) {
			t.Fatalf("page %d = %s", pageNumber, data)
		}
		for _, edge := range result.Viewer.Sessions.Edges {
			seen = append(seen, edge.Node.ID)
		}
		after = result.Viewer.Sessions.Edges[1].Cursor
	}
	want := []string{ids[1], ids[2], ids[0], ids[3]}
	for i, id := range want {
		if seen[i] != relayid.Encode(relayid.Session, id) {
			t.Fatalf("session traversal=%#v, want #%d %q", seen, i, relayid.Encode(relayid.Session, id))
		}
	}
}

func intPointer(value int) *int                 { return &value }
func cursorPointer(value string) *scalar.Cursor { converted := scalar.Cursor(value); return &converted }
