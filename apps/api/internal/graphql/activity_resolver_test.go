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
	appactivity "github.com/moreal/jandibat.org/apps/api/internal/application/activity"
	domain "github.com/moreal/jandibat.org/apps/api/internal/domain/activity"
	"github.com/moreal/jandibat.org/apps/api/internal/graphql/generated"
	"github.com/moreal/jandibat.org/apps/api/internal/graphql/model"
	"github.com/moreal/jandibat.org/apps/api/internal/graphql/relayid"
	"github.com/moreal/jandibat.org/apps/api/internal/graphql/scalar"
	"github.com/moreal/jandibat.org/apps/api/internal/subjects"
	"github.com/vektah/gqlparser/v2/gqlerror"
)

type snapshotSubjectPort struct {
	value      subjects.Subject
	err        error
	actor      string
	id         string
	calls      int
	owned      bool
	ownerActor string
	ownerID    string
	ownerCalls int
	ownerErr   error
}

func (port *snapshotSubjectPort) GetSubject(_ context.Context, actor, id string) (subjects.Subject, error) {
	port.actor, port.id, port.calls = actor, id, port.calls+1
	return port.value, port.err
}

func (port *snapshotSubjectPort) OwnsSubject(_ context.Context, actor, id string) (bool, error) {
	port.ownerActor, port.ownerID, port.ownerCalls = actor, id, port.ownerCalls+1
	return port.owned, port.ownerErr
}

func TestSubjectQueryResolvesRawHandleOrLocalIDWithoutExposingPrivateSubjects(t *testing.T) {
	for _, tc := range []struct {
		name, input, actor, wantLookup string
		lookupErr                      error
		wantID                         string
	}{
		{"raw handle", "alice", "", "alice", nil, relayid.Encode(relayid.Subject, "subject-1")},
		{"ID-shaped valid handle", relayid.Encode(relayid.Subject, "subject-1"), "owner", relayid.Encode(relayid.Subject, "subject-1"), nil, relayid.Encode(relayid.Subject, "subject-1")},
		{"private denied", "alice", "other", "alice", subjects.ErrForbidden, ""},
		{"missing", "missing", "other", "missing", subjects.ErrNotFound, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			port := &snapshotSubjectPort{value: subjects.Subject{ID: "subject-1", Handle: "alice", IsPublic: true}, err: tc.lookupErr}
			ctx := ContextWithNodeServices(context.Background(), NodeServices{Subjects: port})
			if tc.actor != "" {
				ctx = ContextWithVerifiedViewer(ctx, tc.actor)
			}
			got, err := (&queryResolver{&Resolver{}}).Subject(ctx, tc.input)
			if err != nil {
				t.Fatalf("Subject error = %v", err)
			}
			if port.calls != 1 || port.actor != tc.actor || port.id != tc.wantLookup {
				t.Fatalf("lookup = %#v", port)
			}
			if tc.wantID == "" {
				if got != nil {
					t.Fatalf("Subject = %#v, want nil", got)
				}
			} else if got == nil || got.ID != tc.wantID {
				t.Fatalf("Subject = %#v, want ID %q", got, tc.wantID)
			}
		})
	}
}

func TestSubjectQueryDoesNotDowngradeFailedAuthentication(t *testing.T) {
	port := &snapshotSubjectPort{value: subjects.Subject{ID: "subject-1", IsPublic: true}}
	ctx := ContextWithNodeServices(ContextWithFailedAuthentication(context.Background()), NodeServices{Subjects: port})
	got, err := (&queryResolver{&Resolver{}}).Subject(ctx, "alice")
	if got != nil || !errors.Is(err, errNodeAuthentication) || port.calls != 0 {
		t.Fatalf("failed authentication result = (%#v, %v), lookup calls %d", got, err, port.calls)
	}
}

var _ model.Node = (*model.Subject)(nil)

type snapshotActivityPort struct {
	output appactivity.SnapshotOutput
	err    error
	input  appactivity.SnapshotInput
	calls  int
}

func (port *snapshotActivityPort) ExecuteSnapshot(_ context.Context, input appactivity.SnapshotInput) (appactivity.SnapshotOutput, error) {
	port.input, port.calls = input, port.calls+1
	return port.output, port.err
}

func TestActivitySnapshotGraphQLProjectsProvenanceAnd64BitCounts(t *testing.T) {
	updated := time.Date(2024, 3, 1, 9, 0, 0, 0, time.UTC)
	generatedAt := updated.Add(time.Hour)
	owner := domain.SubjectID("subject-1")
	activity := &snapshotActivityPort{output: appactivity.SnapshotOutput{
		GetTimelineOutput: appactivity.GetTimelineOutput{
			From: "2024-02-29", To: "2024-03-01",
			Timeline: domain.Timeline{Subject: owner, Timezone: "UTC", Environments: []domain.Environment{{ID: "github", Key: "github", Name: "GitHub", Scope: domain.EnvironmentScopeGlobal}}, Days: []domain.Day{
				{Date: "2024-02-29", Count: 2147483648, Level: domain.LevelLow, Entries: []domain.DayEntry{{EnvironmentID: "github", Action: domain.ActionCommit, Metric: domain.Metric{Name: domain.MetricCount, Value: 2147483648}}}},
				{Date: "2024-03-01", Count: 1, Level: domain.LevelLow, Entries: []domain.DayEntry{{EnvironmentID: "github", Action: domain.ActionCommit, Metric: domain.Metric{Name: domain.MetricCount, Value: 1}}}},
			}},
		}, GeneratedAt: generatedAt, DataUpdatedAt: &updated, Revision: "sha256:fixture",
	}}
	subjectsPort := &snapshotSubjectPort{value: subjects.Subject{ID: "subject-1", Handle: "alice", IsPublic: true}, owned: true}
	server := handler.NewDefaultServer(generated.NewExecutableSchema(generated.Config{Resolvers: &Resolver{}}))
	graph := client.New(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		ctx := ContextWithNodeServices(ContextWithVerifiedViewer(request.Context(), "owner"), NodeServices{Subjects: subjectsPort, Activity: activity})
		server.ServeHTTP(w, request.WithContext(ctx))
	}))
	result, err := graph.RawPost(`query Snapshot($who: String!, $range: DateRangeInput!, $zone: TimeZone!) {
  subject(handleOrID: $who) { id activitySnapshot(range: $range, timezone: $zone, environmentIDs: ["github", "github"]) {
    subject { id } range { from to } total longestStreak generatedAt dataUpdatedAt revision
    environments { id key name scope metadata { key value } }
    days { date count level entries { environmentID action metricName metricValue metadata { key value } } }
  } }
}`, client.Var("who", "alice"), client.Var("range", map[string]string{"from": "2024-02-29", "to": "2024-03-01"}), client.Var("zone", "UTC"))
	if err != nil || len(result.Errors) != 0 {
		t.Fatalf("GraphQL snapshot = (%#v, %v)", result, err)
	}
	encoded, err := json.Marshal(result.Data)
	if err != nil {
		t.Fatal(err)
	}
	for _, value := range []string{relayid.Encode(relayid.Subject, "subject-1"), `"count":"2147483648"`, `"metricValue":"2147483648"`, `"total":"2147483649"`, `"longestStreak":2`, `"dataUpdatedAt":"2024-03-01T09:00:00Z"`, `"generatedAt":"2024-03-01T10:00:00Z"`, `"revision":"sha256:fixture"`} {
		if !strings.Contains(string(encoded), value) {
			t.Fatalf("snapshot missing %q: %s", value, encoded)
		}
	}
	if activity.calls != 1 || activity.input.Subject != owner || activity.input.ProviderSubject != "alice" || activity.input.Audience != appactivity.AudienceOwner || activity.input.Timezone != "UTC" || len(activity.input.EnvironmentIDs) != 2 {
		t.Fatalf("snapshot input = %#v; calls=%d", activity.input, activity.calls)
	}
}

func TestActivitySnapshotMalformedParentIDFailsBeforeActivity(t *testing.T) {
	activity := &snapshotActivityPort{}
	ctx := ContextWithNodeServices(context.Background(), NodeServices{Activity: activity})
	got, err := (&subjectResolver{&Resolver{}}).ActivitySnapshot(ctx, &model.Subject{ID: relayid.Encode(relayid.Session, "other")}, model.DateRangeInput{From: scalar.Date("2024-01-01"), To: scalar.Date("2024-01-02")}, scalar.TimeZone("UTC"), nil)
	if got != nil || !errors.Is(err, errInvalidNodeID) || activity.calls != 0 {
		t.Fatalf("wrong kind parent = (%#v, %v); activity calls %d", got, err, activity.calls)
	}
}

func TestActivitySnapshotDerivesAudienceOnlyFromVerifiedOwnership(t *testing.T) {
	for _, tc := range []struct {
		name, actor    string
		owned          bool
		want           appactivity.AudienceScope
		wantOwnerCalls int
	}{
		{"anonymous", "", false, appactivity.AudienceAnonymous, 0},
		{"authenticated nonowner", "other", false, appactivity.AudienceAuthenticatedNonOwner, 1},
		{"owner", "owner", true, appactivity.AudienceOwner, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			port := &snapshotSubjectPort{value: subjects.Subject{ID: "subject-1", Handle: "alice", IsPublic: true}, owned: tc.owned}
			activity := &snapshotActivityPort{output: appactivity.SnapshotOutput{GetTimelineOutput: appactivity.GetTimelineOutput{Timeline: domain.Timeline{Subject: "subject-1"}}, Revision: "r"}}
			ctx := context.Background()
			if tc.actor != "" {
				ctx = ContextWithVerifiedViewer(ctx, tc.actor)
			}
			ctx = ContextWithNodeServices(ctx, NodeServices{Subjects: port, Activity: activity})
			parent := &model.Subject{ID: relayid.Encode(relayid.Subject, "subject-1")}
			got, err := (&subjectResolver{&Resolver{}}).ActivitySnapshot(ctx, parent, model.DateRangeInput{From: "2024-02-29", To: "2024-03-01"}, "UTC", []string{"github"})
			if err != nil || got == nil {
				t.Fatalf("ActivitySnapshot = (%#v, %v)", got, err)
			}
			if activity.calls != 1 || activity.input.Audience != tc.want || activity.input.Subject != "subject-1" || activity.input.ProviderSubject != "alice" || activity.input.IncludeSubjectEnvironments || port.calls != 1 || port.actor != tc.actor || port.id != "subject-1" || port.ownerCalls != tc.wantOwnerCalls {
				t.Fatalf("authorization/forwarding = activity %#v; subject %#v", activity, port)
			}
			if tc.wantOwnerCalls > 0 && (port.ownerActor != tc.actor || port.ownerID != "subject-1") {
				t.Fatalf("ownership checked against wrong identity: %#v", port)
			}
			if got.DataUpdatedAt != nil {
				t.Fatalf("never-incorporated dataUpdatedAt = %#v, want nil", got.DataUpdatedAt)
			}
		})
	}
}

func TestActivitySnapshotDoesNotReadPrivateOrFailedAuthentication(t *testing.T) {
	for _, tc := range []struct {
		name            string
		ctx             context.Context
		lookupErr       error
		wantErr         error
		wantLookupCalls int
	}{
		{"anonymous private", context.Background(), subjects.ErrForbidden, nil, 1},
		{"nonowner private", ContextWithVerifiedViewer(context.Background(), "other"), subjects.ErrForbidden, nil, 1},
		{"missing", context.Background(), subjects.ErrNotFound, nil, 1},
		{"failed authentication", ContextWithFailedAuthentication(context.Background()), nil, errNodeAuthentication, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			port := &snapshotSubjectPort{value: subjects.Subject{ID: "subject-1", Handle: "alice"}, err: tc.lookupErr}
			activity := &snapshotActivityPort{}
			ctx := ContextWithNodeServices(tc.ctx, NodeServices{Subjects: port, Activity: activity})
			got, err := (&subjectResolver{&Resolver{}}).ActivitySnapshot(ctx, &model.Subject{ID: relayid.Encode(relayid.Subject, "subject-1")}, model.DateRangeInput{From: "2024-02-29", To: "2024-03-01"}, "UTC", nil)
			if got != nil || !errors.Is(err, tc.wantErr) || activity.calls != 0 || port.calls != tc.wantLookupCalls || port.ownerCalls != 0 {
				t.Fatalf("private/failed result = (%#v, %v); subject %#v; activity %#v", got, err, port, activity)
			}
		})
	}
}

func TestActivitySnapshotMapsValidationErrorsWithoutInternalMessages(t *testing.T) {
	for _, tc := range []struct {
		err  error
		code string
	}{
		{appactivity.ErrInvalidDateRange, "INVALID_DATE_RANGE"},
		{appactivity.ErrDateRangeTooLarge, "RANGE_TOO_LARGE"},
		{appactivity.ErrInvalidProvider, "INVALID_ENVIRONMENT"},
		{appactivity.ErrProviderUnavailable, "PROVIDER_UNAVAILABLE"},
	} {
		port := &snapshotSubjectPort{value: subjects.Subject{ID: "subject-1", Handle: "alice", IsPublic: true}}
		activity := &snapshotActivityPort{err: tc.err}
		ctx := ContextWithNodeServices(context.Background(), NodeServices{Subjects: port, Activity: activity})
		_, err := (&subjectResolver{&Resolver{}}).ActivitySnapshot(ctx, &model.Subject{ID: relayid.Encode(relayid.Subject, "subject-1")}, model.DateRangeInput{From: "2024-02-29", To: "2024-03-01"}, "UTC", nil)
		var public *gqlerror.Error
		if !errors.As(err, &public) || public.Extensions["code"] != tc.code || strings.Contains(public.Message, "activity application") {
			t.Fatalf("error %v mapped to %#v, want code %q", tc.err, err, tc.code)
		}
	}
	secret := errors.New("database DSN is private")
	if got := snapshotActivityError(secret); !errors.Is(got, errNodeLookup) || strings.Contains(got.Error(), "DSN") {
		t.Fatalf("internal error leaked: %v", got)
	}
}
