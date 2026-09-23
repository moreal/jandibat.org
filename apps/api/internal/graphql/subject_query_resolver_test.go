package graphql

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/99designs/gqlgen/client"
	"github.com/moreal/jandibat.org/apps/api/internal/graphql/cursor"
	"github.com/moreal/jandibat.org/apps/api/internal/graphql/relayid"
	"github.com/moreal/jandibat.org/apps/api/internal/subjects"
)

func TestSubjectReadProjectsPublicProfileFromService(t *testing.T) {
	now := time.Date(2026, 9, 1, 12, 30, 0, 0, time.UTC)
	name := "Public profile"
	port := nodeSubjectPort{subject: subjects.Subject{ID: "subject-1", Handle: "public", DisplayName: &name, Timezone: "Asia/Seoul", IsPublic: true, CreatedAt: now, UpdatedAt: now}}
	graph := viewerGraphQLClient(context.Background(), NodeServices{Subjects: port})
	response, err := graph.RawPost(`query($id: ID!) { byHandle: subject(handleOrID: "public") { id handle displayName timezone isPublic createdAt updatedAt } byNode: node(id: $id) { ... on Subject { id handle displayName timezone isPublic createdAt updatedAt } } }`, client.Var("id", relayid.Encode(relayid.Subject, "subject-1")))
	if err != nil || len(response.Errors) != 0 {
		t.Fatalf("public subject profile = (%#v, %v)", response, err)
	}
	data, _ := json.Marshal(response.Data)
	for _, want := range []string{`"handle":"public"`, `"displayName":"Public profile"`, `"timezone":"Asia/Seoul"`, `"isPublic":true`, `"createdAt":"2026-09-01T12:30:00Z"`, `"updatedAt":"2026-09-01T12:30:00Z"`, `"id":"` + relayid.Encode(relayid.Subject, "subject-1") + `"`} {
		if strings.Count(string(data), want) != 2 {
			t.Fatalf("public profile missing twice %s: %s", want, data)
		}
	}
}

type subjectQueryPort struct {
	mu                   sync.Mutex
	page                 subjects.SubjectPage
	userSettings         subjects.UserSettings
	subjectSettings      subjects.SubjectSettings
	pageActor            string
	pageAfter            *subjects.SubjectCursor
	pageFirst            int
	settingsActor        string
	settingsSubject      string
	pageCalls            int
	userSettingsCalls    int
	subjectSettingsCalls int
}

func (port *subjectQueryPort) ListSubjectsPage(_ context.Context, actor string, after *subjects.SubjectCursor, first int) (subjects.SubjectPage, error) {
	port.mu.Lock()
	defer port.mu.Unlock()
	port.pageActor, port.pageAfter, port.pageFirst, port.pageCalls = actor, after, first, port.pageCalls+1
	return port.page, nil
}

func (port *subjectQueryPort) GetUserSettings(_ context.Context, actor string) (subjects.UserSettings, error) {
	port.mu.Lock()
	defer port.mu.Unlock()
	port.settingsActor, port.userSettingsCalls = actor, port.userSettingsCalls+1
	return port.userSettings, nil
}

func (port *subjectQueryPort) GetSubjectSettings(_ context.Context, actor, subjectID string) (subjects.SubjectSettings, error) {
	port.mu.Lock()
	defer port.mu.Unlock()
	port.settingsActor, port.settingsSubject, port.subjectSettingsCalls = actor, subjectID, port.subjectSettingsCalls+1
	return port.subjectSettings, nil
}

func TestSubjectReadOwnerSettingsAndTypedPage(t *testing.T) {
	now := time.Date(2026, 9, 1, 12, 30, 0, 0, time.UTC)
	name := "Mine"
	owner := subjects.Subject{ID: "subject-1", OwnerUserID: "owner", Handle: "mine", DisplayName: &name, Timezone: "UTC", IsPublic: false, CreatedAt: now, UpdatedAt: now}
	port := &subjectQueryPort{
		page:            subjects.SubjectPage{Subjects: []subjects.Subject{owner}, HasNextPage: true},
		userSettings:    subjects.UserSettings{Locale: "ko-KR", Timezone: "Asia/Seoul", Theme: subjects.ThemeGitHubDark, UpdatedAt: now},
		subjectSettings: subjects.SubjectSettings{SubjectID: owner.ID, Timezone: "UTC", IsPublic: false, DefaultTheme: subjects.ThemeDark, WeekStart: subjects.WeekStartMonday, SyncEnabled: true, SyncIntervalMinutes: 30, FailurePolicy: subjects.FailurePurge, UpdatedAt: now},
	}
	users := &viewerUserPort{value: subjects.User{ID: "owner", Status: subjects.UserStatusActive, CreatedAt: now, UpdatedAt: now}}
	ctx := ContextWithVerifiedViewer(context.Background(), "owner")
	ctx = ContextWithSubjectQueryServices(ctx, SubjectQueryServices{Pages: port, UserSettings: port, SubjectSettings: port})
	graph := viewerGraphQLClient(ctx, NodeServices{ViewerUsers: users, Subjects: nodeSubjectPort{subject: owner, owned: true}})
	response, err := graph.RawPost(`query { viewer { settings { locale timezone theme updatedAt } subjects(first: 1) { edges { cursor node { id handle settings { timezone isPublic defaultTheme weekStart syncEnabled syncIntervalMinutes failurePolicy updatedAt } } } pageInfo { hasNextPage hasPreviousPage startCursor endCursor } } } }`)
	if err != nil || len(response.Errors) != 0 {
		t.Fatalf("owner subject page = (%#v, %v)", response, err)
	}
	data, _ := json.Marshal(response.Data)
	for _, want := range []string{`"locale":"ko-KR"`, `"theme":"GITHUB_DARK"`, `"handle":"mine"`, `"defaultTheme":"DARK"`, `"weekStart":"MONDAY"`, `"failurePolicy":"PURGE"`, `"syncIntervalMinutes":30`, `"hasNextPage":true`, `"hasPreviousPage":false`} {
		if !strings.Contains(string(data), want) {
			t.Fatalf("owner projection missing %s: %s", want, data)
		}
	}
	if port.pageCalls != 1 || port.pageActor != "owner" || port.pageFirst != 1 || port.pageAfter != nil || port.userSettingsCalls != 1 || port.subjectSettingsCalls != 1 || port.settingsActor != "owner" || port.settingsSubject != owner.ID {
		t.Fatalf("owner ports = %#v", port)
	}
	var parsed struct {
		Viewer struct {
			Subjects struct {
				Edges []struct {
					Cursor string `json:"cursor"`
				}
				PageInfo struct {
					StartCursor string `json:"startCursor"`
					EndCursor   string `json:"endCursor"`
				} `json:"pageInfo"`
			} `json:"subjects"`
		} `json:"viewer"`
	}
	if err := json.Unmarshal(data, &parsed); err != nil {
		t.Fatal(err)
	}
	if len(parsed.Viewer.Subjects.Edges) != 1 || parsed.Viewer.Subjects.Edges[0].Cursor != parsed.Viewer.Subjects.PageInfo.StartCursor || parsed.Viewer.Subjects.Edges[0].Cursor != parsed.Viewer.Subjects.PageInfo.EndCursor {
		t.Fatalf("cursor boundary: %s", data)
	}
	position, err := cursor.DecodeAs(cursor.Subject, parsed.Viewer.Subjects.Edges[0].Cursor)
	if err != nil || position.ID != owner.ID || !position.Timestamp.Equal(now) {
		t.Fatalf("subject cursor = (%#v,%v)", position, err)
	}
}

func TestSubjectReadSettingsNeverLeakToAnonymousOrNonOwner(t *testing.T) {
	now := time.Date(2026, 9, 1, 12, 30, 0, 0, time.UTC)
	subject := subjects.Subject{ID: "subject-1", Handle: "public", Timezone: "UTC", IsPublic: true, CreatedAt: now, UpdatedAt: now}
	port := &subjectQueryPort{subjectSettings: subjects.SubjectSettings{SubjectID: subject.ID, Timezone: "UTC", DefaultTheme: subjects.ThemeSystem, WeekStart: subjects.WeekStartSunday, SyncIntervalMinutes: 60, FailurePolicy: subjects.FailureKeepStale, UpdatedAt: now}}
	for _, ctx := range []context.Context{context.Background(), ContextWithVerifiedViewer(context.Background(), "other")} {
		ctx = ContextWithSubjectQueryServices(ctx, SubjectQueryServices{SubjectSettings: port})
		graph := viewerGraphQLClient(ctx, NodeServices{Subjects: nodeSubjectPort{subject: subject, owned: false}})
		response, err := graph.RawPost(`query { subject(handleOrID: "public") { id settings { timezone } } }`)
		if err != nil || len(response.Errors) != 0 {
			t.Fatalf("non-owner settings = (%#v,%v)", response, err)
		}
		data, _ := json.Marshal(response.Data)
		if !strings.Contains(string(data), `"settings":null`) {
			t.Fatalf("settings leaked: %s", data)
		}
	}
	if port.subjectSettingsCalls != 0 {
		t.Fatalf("settings port called without ownership: %#v", port)
	}
}

func TestSubjectReadRejectsWrongCursorKindBeforeListCall(t *testing.T) {
	now := time.Date(2026, 9, 1, 12, 30, 0, 0, time.UTC)
	wrong, err := cursor.Encode(cursor.SyncJob, cursor.Position{Timestamp: now, ID: "550e8400-e29b-41d4-a716-446655440011"})
	if err != nil {
		t.Fatal(err)
	}
	port := &subjectQueryPort{}
	users := &viewerUserPort{value: subjects.User{ID: "owner", Status: subjects.UserStatusActive}}
	ctx := ContextWithSubjectQueryServices(ContextWithVerifiedViewer(context.Background(), "owner"), SubjectQueryServices{Pages: port})
	graph := viewerGraphQLClient(ctx, NodeServices{ViewerUsers: users})
	response, err := graph.RawPost(`query($after: Cursor!) { viewer { subjects(after: $after) { edges { cursor } } } }`, client.Var("after", wrong))
	if err != nil || !strings.Contains(string(response.Errors), `"code":"BAD_USER_INPUT"`) || port.pageCalls != 0 {
		t.Fatalf("wrong cursor response = (%#v,%v); page calls=%d", response, err, port.pageCalls)
	}
}

func TestSubjectReadForwardsDeletedTieAnchorAndBoundsPageSize(t *testing.T) {
	now := time.Date(2026, 9, 1, 12, 30, 0, 0, time.UTC)
	anchor, err := cursor.Encode(cursor.Subject, cursor.Position{Timestamp: now, ID: "deleted-subject"})
	if err != nil {
		t.Fatal(err)
	}
	port := &subjectQueryPort{page: subjects.SubjectPage{Subjects: []subjects.Subject{{ID: "later-subject", OwnerUserID: "owner", Handle: "later", Timezone: "UTC", CreatedAt: now, UpdatedAt: now}}}}
	users := &viewerUserPort{value: subjects.User{ID: "owner", Status: subjects.UserStatusActive}}
	ctx := ContextWithSubjectQueryServices(ContextWithVerifiedViewer(context.Background(), "owner"), SubjectQueryServices{Pages: port})
	graph := viewerGraphQLClient(ctx, NodeServices{ViewerUsers: users})
	response, err := graph.RawPost(`query($after: Cursor!) { viewer { subjects(first: 1, after: $after) { edges { node { id handle } } pageInfo { hasPreviousPage hasNextPage } } } }`, client.Var("after", anchor))
	if err != nil || len(response.Errors) != 0 {
		t.Fatalf("deleted anchor response = (%#v,%v)", response, err)
	}
	data, _ := json.Marshal(response.Data)
	if port.pageCalls != 1 || port.pageActor != "owner" || port.pageAfter == nil || port.pageAfter.ID != "deleted-subject" || !port.pageAfter.CreatedAt.Equal(now) || port.pageFirst != 1 || !strings.Contains(string(data), `"handle":"later"`) || !strings.Contains(string(data), `"hasPreviousPage":true`) {
		t.Fatalf("deleted anchor forwarding = %s, port=%#v", data, port)
	}
	for _, size := range []int{0, -1, 101} {
		response, err := graph.RawPost(`query($first: Int!) { viewer { subjects(first: $first) { edges { cursor } } } }`, client.Var("first", size))
		if err != nil || !strings.Contains(string(response.Errors), `"code":"BAD_USER_INPUT"`) || port.pageCalls != 1 {
			t.Fatalf("first=%d response=(%#v,%v), calls=%d", size, response, err, port.pageCalls)
		}
	}
}
