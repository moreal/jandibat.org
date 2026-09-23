//go:build integration

package cockroach_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"errors"
	"net/url"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	integrationstore "github.com/moreal/jandibat.org/apps/api/internal/adapters/integrations/cockroach"
	activitystore "github.com/moreal/jandibat.org/apps/api/internal/adapters/storage/cockroach"
	"github.com/moreal/jandibat.org/apps/api/internal/domain/activity"
	"github.com/moreal/jandibat.org/apps/api/internal/integrations"
)

type customIngestRaceFixture struct {
	ctx        context.Context
	db         *sql.DB
	pool       *pgxpool.Pool
	store      *integrationstore.Store
	providerID string
	subjectID  string
	secret     string
}

func newCustomIngestRaceFixture(t *testing.T) customIngestRaceFixture {
	t.Helper()
	dsn := os.Getenv("JANDIBAT_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("set JANDIBAT_TEST_DATABASE_URL to a migrated isolated CockroachDB")
	}
	apiDSN := os.Getenv("JANDIBAT_TEST_API_DATABASE_URL")
	if apiDSN == "" {
		parsed, err := url.Parse(dsn)
		if err != nil {
			t.Fatal(err)
		}
		parsed.User = url.User("jandibat_api")
		apiDSN = parsed.String()
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	t.Cleanup(cancel)
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := db.PingContext(ctx); err != nil {
		t.Fatal(err)
	}
	pool, err := pgxpool.New(ctx, apiDSN)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	store, err := integrationstore.New(pool)
	if err != nil {
		t.Fatal(err)
	}

	suffix := integrationSuffix(t)
	userID := "it_race_user_" + suffix
	subjectID := "it_race_subject_" + suffix
	environmentID := "it_race_environment_" + suffix
	providerID := uuid.NewString()
	_, err = db.ExecContext(ctx, `INSERT INTO users (id, primary_email, status) VALUES ($1, $2, 'active')`, userID, userID+"@example.invalid")
	if err != nil {
		t.Fatalf("create fixture user: %v", err)
	}
	t.Cleanup(func() {
		cleanupCtx, done := context.WithTimeout(context.Background(), 10*time.Second)
		defer done()
		_, _ = db.ExecContext(cleanupCtx, `DELETE FROM custom_providers WHERE id = $1`, providerID)
		_, _ = db.ExecContext(cleanupCtx, `DELETE FROM environments WHERE id = $1`, environmentID)
		_, _ = db.ExecContext(cleanupCtx, `DELETE FROM subjects WHERE id = $1`, subjectID)
		_, _ = db.ExecContext(cleanupCtx, `DELETE FROM users WHERE id = $1`, userID)
	})
	_, err = db.ExecContext(ctx, `INSERT INTO subjects (id, owner_user_id, handle, timezone) VALUES ($1, $2, $3, 'UTC')`, subjectID, userID, "it-race-"+suffix)
	if err != nil {
		t.Fatalf("create fixture subject: %v", err)
	}
	_, err = db.ExecContext(ctx, `INSERT INTO environments (id, key, name, scope, owner_subject_id) VALUES ($1, $2, 'Race fixture', 'subject', $3)`, environmentID, environmentID, subjectID)
	if err != nil {
		t.Fatalf("create fixture environment: %v", err)
	}
	_, err = db.ExecContext(ctx, `INSERT INTO custom_providers (id, owner_user_id, subject_id, environment_id, slug, name, status, configuration) VALUES ($1, $2, $3, $4, $5, 'Race fixture', 'active', '{"allowed_actions":["read"],"allowed_metrics":["count"]}'::JSONB)`, providerID, userID, subjectID, environmentID, "it_race_"+suffix)
	if err != nil {
		t.Fatalf("create fixture provider: %v", err)
	}
	secret := strings.Repeat("s", 32)
	digest := sha256.Sum256([]byte(secret))
	if _, err := db.ExecContext(ctx, `INSERT INTO custom_provider_secrets (provider_id, ingest_token_hash) VALUES ($1, $2)`, providerID, digest[:]); err != nil {
		t.Fatalf("create fixture secret digest: %v", err)
	}
	return customIngestRaceFixture{ctx: ctx, db: db, pool: pool, store: store, providerID: providerID, subjectID: subjectID, secret: secret}
}

type staticIngestRaceClock struct{ now time.Time }

func (clock staticIngestRaceClock) Now() time.Time { return clock.now }

type pausedIngestOutcome struct {
	result integrations.IngestCustomActivitiesResult
	err    error
}

type pauseAfterReservationStore struct {
	*integrationstore.Store
	reserved chan struct{}
	resume   chan struct{}
}

func (store *pauseAfterReservationStore) CreateIngestIdempotencyKey(ctx context.Context, record integrations.IngestIdempotencyRecord) (bool, error) {
	created, err := store.Store.CreateIngestIdempotencyKey(ctx, record)
	if err == nil && created {
		close(store.reserved)
		select {
		case <-store.resume:
		case <-ctx.Done():
			return false, ctx.Err()
		}
	}
	return created, err
}

type pauseAfterCredentialReadStore struct {
	*integrationstore.Store
	read   chan struct{}
	resume chan struct{}
}

func (store *pauseAfterCredentialReadStore) GetCustomProvider(ctx context.Context, id string) (integrations.CustomProviderRecord, error) {
	record, err := store.Store.GetCustomProvider(ctx, id)
	if err == nil {
		close(store.read)
		select {
		case <-store.resume:
		case <-ctx.Done():
			return integrations.CustomProviderRecord{}, ctx.Err()
		}
	}
	return record, err
}

type rollbackAfterFactsSink struct {
	*activitystore.Store
	failure error
}

type nonTransactionalIngestSink struct{}

func (nonTransactionalIngestSink) SaveEnvironments(context.Context, activity.SaveEnvironmentsInput) error {
	return nil
}

func (nonTransactionalIngestSink) SaveFacts(context.Context, activity.SaveFactsInput) error {
	return nil
}

func (sink rollbackAfterFactsSink) SaveFactsInCurrentTransaction(ctx context.Context, input activity.SaveFactsInput) error {
	if err := sink.Store.SaveFactsInCurrentTransaction(ctx, input); err != nil {
		return err
	}
	return sink.failure
}

func newCustomIngestRaceService(t *testing.T, fixture customIngestRaceFixture, store integrations.CustomProviderStore, sink integrations.ActivitySink, now time.Time) *integrations.CustomProviderService {
	t.Helper()
	if sink == nil {
		var err error
		sink, err = activitystore.New(fixture.pool)
		if err != nil {
			t.Fatal(err)
		}
	}
	cipher, err := integrations.NewAESGCMCipher(bytes.Repeat([]byte{0x5a}, 32))
	if err != nil {
		t.Fatal(err)
	}
	service, err := integrations.NewCustomProviderService(store, cipher, sink, staticIngestRaceClock{now: now}, nil)
	if err != nil {
		t.Fatal(err)
	}
	return service
}

func countCustomIngestRaceWrites(t *testing.T, fixture customIngestRaceFixture) (events, facts int) {
	t.Helper()
	if err := fixture.db.QueryRowContext(fixture.ctx, `SELECT count(*) FROM custom_activity_events WHERE custom_provider_id = $1`, fixture.providerID).Scan(&events); err != nil {
		t.Fatal(err)
	}
	if err := fixture.db.QueryRowContext(fixture.ctx, `SELECT count(*) FROM activity_facts WHERE custom_provider_id = $1`, fixture.providerID).Scan(&facts); err != nil {
		t.Fatal(err)
	}
	return events, facts
}

func TestCockroachDisabledProviderRejectsStaleActivityWrite(t *testing.T) {
	fixture := newCustomIngestRaceFixture(t)
	if _, err := fixture.db.ExecContext(fixture.ctx, `UPDATE custom_providers SET status = 'disabled' WHERE id = $1`, fixture.providerID); err != nil {
		t.Fatal(err)
	}
	_, err := fixture.store.SaveIngestedActivities(fixture.ctx, []integrations.IngestedActivity{{
		ProviderID: fixture.providerID, SubjectID: fixture.subjectID, ExternalID: "late-event",
		Date: time.Now().UTC().Format(time.DateOnly), Action: "read", Metric: "count",
		Value: 1, Metadata: map[string]string{}, IngestedAt: time.Now().UTC(),
	}})
	if !errors.Is(err, integrations.ErrProviderDisabled) {
		t.Fatalf("disabled provider write error = %v, want ErrProviderDisabled", err)
	}
	var count int
	if err := fixture.db.QueryRowContext(fixture.ctx, `SELECT count(*) FROM custom_activity_events WHERE custom_provider_id = $1`, fixture.providerID).Scan(&count); err != nil || count != 0 {
		t.Fatalf("events after disabled provider write = (%d, %v), want zero", count, err)
	}
}

func reserveExpiredThenReacquire(t *testing.T, fixture customIngestRaceFixture) (integrations.IngestIdempotencyRecord, integrations.IngestIdempotencyRecord) {
	t.Helper()
	keyHash := sha256.Sum256([]byte("race-key-" + fixture.providerID))
	requestHash := sha256.Sum256([]byte("same-request-" + fixture.providerID))
	now := time.Now().UTC().Truncate(time.Microsecond)
	old := integrations.IngestIdempotencyRecord{
		ProviderID: fixture.providerID, KeyHash: keyHash[:], RequestHash: requestHash[:],
		ReservationToken: uuid.NewString(),
		ResponseStatus:   0, ResponseBody: json.RawMessage(`{}`),
		CreatedAt: now.Add(-2 * time.Hour), ExpiresAt: now.Add(-time.Hour),
	}
	created, err := fixture.store.CreateIngestIdempotencyKey(fixture.ctx, old)
	if err != nil || !created {
		t.Fatalf("reserve expired owner = (%t, %v)", created, err)
	}
	fresh := old
	fresh.ReservationToken = uuid.NewString()
	fresh.CreatedAt = now
	fresh.ExpiresAt = now.Add(time.Hour)
	created, err = fixture.store.CreateIngestIdempotencyKey(fixture.ctx, fresh)
	if err != nil || !created {
		t.Fatalf("reacquire same key/request after expiry = (%t, %v)", created, err)
	}
	return old, fresh
}

func TestCockroachStaleIngestOwnerCannotCompleteReacquiredReservation(t *testing.T) {
	fixture := newCustomIngestRaceFixture(t)
	stale, fresh := reserveExpiredThenReacquire(t, fixture)
	stale.ResponseStatus = 202
	stale.ResponseBody = json.RawMessage(`{"accepted":1}`)
	stale.ExpiresAt = time.Now().UTC().Add(time.Hour)
	completed, err := fixture.store.CompleteIngestIdempotencyKey(fixture.ctx, stale)
	if err != nil {
		t.Fatal(err)
	}
	if completed {
		t.Fatal("stale owner completed a newer reservation with the same request hash")
	}
	current, found, err := fixture.store.GetIngestIdempotencyKey(fixture.ctx, fixture.providerID, stale.KeyHash)
	if err != nil || !found || current.ResponseStatus != 0 || current.ReservationToken != fresh.ReservationToken {
		t.Fatalf("new reservation after stale completion = (found=%t, status=%d, token matches=%t, err=%v)", found, current.ResponseStatus, current.ReservationToken == fresh.ReservationToken, err)
	}
}

func TestCockroachStaleIngestOwnerCannotReleaseReacquiredReservation(t *testing.T) {
	fixture := newCustomIngestRaceFixture(t)
	stale, fresh := reserveExpiredThenReacquire(t, fixture)
	if err := fixture.store.ReleaseIngestIdempotencyKey(fixture.ctx, fixture.providerID, stale.KeyHash, stale.RequestHash, stale.ReservationToken); err != nil {
		t.Fatal(err)
	}
	current, found, err := fixture.store.GetIngestIdempotencyKey(fixture.ctx, fixture.providerID, stale.KeyHash)
	if err != nil || !found || current.ResponseStatus != 0 || current.ReservationToken != fresh.ReservationToken {
		t.Fatalf("new reservation after stale release = (found=%t, status=%d, token matches=%t, err=%v)", found, current.ResponseStatus, current.ReservationToken == fresh.ReservationToken, err)
	}
}

func TestCockroachReplacedReservationCannotWriteEventsOrFacts(t *testing.T) {
	fixture := newCustomIngestRaceFixture(t)
	// A's lease is already expired at the database clock, making the takeover
	// deterministic without sleeping through the production retention period.
	oldNow := time.Now().UTC().Add(-48 * time.Hour).Truncate(time.Microsecond)
	paused := &pauseAfterReservationStore{
		Store: fixture.store, reserved: make(chan struct{}), resume: make(chan struct{}),
	}
	var resumeOnce sync.Once
	resume := func() { resumeOnce.Do(func() { close(paused.resume) }) }
	defer resume()
	realSink, err := activitystore.New(fixture.pool)
	if err != nil {
		t.Fatal(err)
	}
	rollbackProbe := errors.New("rollback after fact write")
	service := newCustomIngestRaceService(t, fixture, paused, rollbackAfterFactsSink{Store: realSink, failure: rollbackProbe}, oldNow)
	input := integrations.IngestCustomActivitiesInput{
		ProviderID: fixture.providerID, IngestSecret: fixture.secret,
		IdempotencyKey: "stale-owner-key", Activities: []integrations.CustomActivity{{
			ExternalID: "stale-owner-event", Date: oldNow.Format(time.DateOnly),
			Action: "read", Metric: "count", Value: 1,
		}},
	}
	outcome := make(chan pausedIngestOutcome, 1)
	go func() {
		result, err := service.Ingest(fixture.ctx, input)
		outcome <- pausedIngestOutcome{result: result, err: err}
	}()
	select {
	case <-paused.reserved:
	case <-fixture.ctx.Done():
		t.Fatalf("wait for first reservation: %v", fixture.ctx.Err())
	}
	keyHash := sha256.Sum256([]byte(input.IdempotencyKey))
	differentRequestHash := sha256.Sum256([]byte("different-payload"))
	now := time.Now().UTC().Truncate(time.Microsecond)
	type reservationOutcome struct {
		created bool
		err     error
	}
	secondStarted := make(chan struct{})
	secondOutcome := make(chan reservationOutcome, 1)
	go func() {
		close(secondStarted)
		created, err := fixture.store.CreateIngestIdempotencyKey(fixture.ctx, integrations.IngestIdempotencyRecord{
			ProviderID: fixture.providerID, KeyHash: keyHash[:], RequestHash: differentRequestHash[:],
			ReservationToken: uuid.NewString(), ResponseStatus: 0, ResponseBody: json.RawMessage(`{}`),
			CreatedAt: now, ExpiresAt: now.Add(time.Hour),
		})
		secondOutcome <- reservationOutcome{created: created, err: err}
	}()
	<-secondStarted
	select {
	case takeover := <-secondOutcome:
		t.Fatalf("second owner acquired before first transaction ended: (%t, %v)", takeover.created, takeover.err)
	case <-time.After(100 * time.Millisecond):
	case <-fixture.ctx.Done():
		t.Fatalf("wait for blocked takeover: %v", fixture.ctx.Err())
	}
	resume()
	select {
	case completed := <-outcome:
		if !errors.Is(completed.err, rollbackProbe) {
			t.Fatalf("failed first ingest error = %v, want rollback probe", completed.err)
		}
	case <-fixture.ctx.Done():
		t.Fatalf("wait for failed first ingest: %v", fixture.ctx.Err())
	}
	select {
	case takeover := <-secondOutcome:
		if takeover.err != nil || !takeover.created {
			t.Fatalf("second owner after first rollback = (%t, %v)", takeover.created, takeover.err)
		}
	case <-fixture.ctx.Done():
		t.Fatalf("wait for second reservation: %v", fixture.ctx.Err())
	}
	events, facts := countCustomIngestRaceWrites(t, fixture)
	if events != 0 || facts != 0 {
		t.Fatalf("failed first owner left events=%d facts=%d after takeover", events, facts)
	}
}

func TestCockroachRotatedIngestSecretRejectsStaleAuthenticatedWrite(t *testing.T) {
	fixture := newCustomIngestRaceFixture(t)
	now := time.Now().UTC().Truncate(time.Microsecond)
	paused := &pauseAfterCredentialReadStore{
		Store: fixture.store, read: make(chan struct{}), resume: make(chan struct{}),
	}
	var resumeOnce sync.Once
	resume := func() { resumeOnce.Do(func() { close(paused.resume) }) }
	defer resume()
	service := newCustomIngestRaceService(t, fixture, paused, nil, now)
	input := integrations.IngestCustomActivitiesInput{
		ProviderID: fixture.providerID, IngestSecret: fixture.secret,
		Activities: []integrations.CustomActivity{{
			ExternalID: "old-secret-event", Date: now.Format(time.DateOnly),
			Action: "read", Metric: "count", Value: 1,
		}},
	}
	outcome := make(chan pausedIngestOutcome, 1)
	go func() {
		result, err := service.Ingest(fixture.ctx, input)
		outcome <- pausedIngestOutcome{result: result, err: err}
	}()
	select {
	case <-paused.read:
	case <-fixture.ctx.Done():
		t.Fatalf("wait for original credential read: %v", fixture.ctx.Err())
	}
	rotatedDigest := sha256.Sum256([]byte(strings.Repeat("n", 32)))
	if _, err := fixture.db.ExecContext(fixture.ctx, `UPDATE custom_provider_secrets SET ingest_token_hash = $1 WHERE provider_id = $2`, rotatedDigest[:], fixture.providerID); err != nil {
		t.Fatalf("rotate fixture secret digest: %v", err)
	}
	resume()
	select {
	case completed := <-outcome:
		if !errors.Is(completed.err, integrations.ErrUnauthorized) {
			t.Fatalf("old-secret ingest error = %v, want unauthorized", completed.err)
		}
	case <-fixture.ctx.Done():
		t.Fatalf("wait for old-secret ingest: %v", fixture.ctx.Err())
	}
	events, facts := countCustomIngestRaceWrites(t, fixture)
	if events != 0 || facts != 0 {
		t.Fatalf("old secret wrote events=%d facts=%d after rotation", events, facts)
	}
}

func TestCockroachDisabledAfterAuthenticationRejectsStaleWrite(t *testing.T) {
	fixture := newCustomIngestRaceFixture(t)
	now := time.Now().UTC().Truncate(time.Microsecond)
	paused := &pauseAfterCredentialReadStore{
		Store: fixture.store, read: make(chan struct{}), resume: make(chan struct{}),
	}
	var resumeOnce sync.Once
	resume := func() { resumeOnce.Do(func() { close(paused.resume) }) }
	defer resume()
	service := newCustomIngestRaceService(t, fixture, paused, nil, now)
	input := integrations.IngestCustomActivitiesInput{
		ProviderID: fixture.providerID, IngestSecret: fixture.secret,
		Activities: []integrations.CustomActivity{{
			ExternalID: "disabled-after-read-event", Date: now.Format(time.DateOnly),
			Action: "read", Metric: "count", Value: 1,
		}},
	}
	outcome := make(chan pausedIngestOutcome, 1)
	go func() {
		result, err := service.Ingest(fixture.ctx, input)
		outcome <- pausedIngestOutcome{result: result, err: err}
	}()
	select {
	case <-paused.read:
	case <-fixture.ctx.Done():
		t.Fatalf("wait for original provider read: %v", fixture.ctx.Err())
	}
	if _, err := fixture.db.ExecContext(fixture.ctx, `UPDATE custom_providers SET status = 'disabled' WHERE id = $1`, fixture.providerID); err != nil {
		t.Fatalf("disable fixture provider: %v", err)
	}
	resume()
	select {
	case completed := <-outcome:
		if !errors.Is(completed.err, integrations.ErrProviderDisabled) {
			t.Fatalf("disabled provider ingest error = %v, want provider disabled", completed.err)
		}
	case <-fixture.ctx.Done():
		t.Fatalf("wait for disabled ingest: %v", fixture.ctx.Err())
	}
	events, facts := countCustomIngestRaceWrites(t, fixture)
	if events != 0 || facts != 0 {
		t.Fatalf("disabled provider wrote events=%d facts=%d after state change", events, facts)
	}
}

func TestCockroachDurableIngestRejectsNonTransactionalFactSink(t *testing.T) {
	fixture := newCustomIngestRaceFixture(t)
	now := time.Now().UTC().Truncate(time.Microsecond)
	service := newCustomIngestRaceService(t, fixture, fixture.store, nonTransactionalIngestSink{}, now)
	_, err := service.Ingest(fixture.ctx, integrations.IngestCustomActivitiesInput{
		ProviderID: fixture.providerID, IngestSecret: fixture.secret,
		IdempotencyKey: "missing-atomic-sink", Activities: []integrations.CustomActivity{{
			ExternalID: "missing-sink-event", Date: now.Format(time.DateOnly),
			Action: "read", Metric: "count", Value: 1,
		}},
	})
	if !errors.Is(err, integrations.ErrAtomicIngestUnavailable) {
		t.Fatalf("nontransactional sink ingest error = %v, want atomic ingest unavailable", err)
	}
	events, facts := countCustomIngestRaceWrites(t, fixture)
	if events != 0 || facts != 0 {
		t.Fatalf("nontransactional sink left events=%d facts=%d", events, facts)
	}
	var reservations int
	if err := fixture.db.QueryRowContext(fixture.ctx, `SELECT count(*) FROM ingest_idempotency_keys WHERE custom_provider_id = $1`, fixture.providerID).Scan(&reservations); err != nil || reservations != 0 {
		t.Fatalf("nontransactional sink reserved key = (%d, %v)", reservations, err)
	}
}

func TestCockroachRotationWaitsForInFlightIngestCommit(t *testing.T) {
	fixture := newCustomIngestRaceFixture(t)
	now := time.Now().UTC().Truncate(time.Microsecond)
	paused := &pauseAfterReservationStore{
		Store: fixture.store, reserved: make(chan struct{}), resume: make(chan struct{}),
	}
	var resumeOnce sync.Once
	resume := func() { resumeOnce.Do(func() { close(paused.resume) }) }
	defer resume()
	service := newCustomIngestRaceService(t, fixture, paused, nil, now)
	input := integrations.IngestCustomActivitiesInput{
		ProviderID: fixture.providerID, IngestSecret: fixture.secret,
		IdempotencyKey: "rotation-order-key", Activities: []integrations.CustomActivity{{
			ExternalID: "rotation-order-event", Date: now.Format(time.DateOnly),
			Action: "read", Metric: "count", Value: 1,
		}},
	}
	outcome := make(chan pausedIngestOutcome, 1)
	go func() {
		result, err := service.Ingest(fixture.ctx, input)
		outcome <- pausedIngestOutcome{result: result, err: err}
	}()
	select {
	case <-paused.reserved:
	case <-fixture.ctx.Done():
		t.Fatalf("wait for in-flight reservation: %v", fixture.ctx.Err())
	}
	rotationStarted := make(chan struct{})
	rotationOutcome := make(chan error, 1)
	go func() {
		close(rotationStarted)
		rotationOutcome <- service.RotateIngestSecret(fixture.ctx, fixture.providerID, strings.Repeat("n", 32))
	}()
	<-rotationStarted
	select {
	case err := <-rotationOutcome:
		t.Fatalf("rotation completed before in-flight ingest released provider lock: %v", err)
	case <-time.After(100 * time.Millisecond):
	case <-fixture.ctx.Done():
		t.Fatalf("wait for blocked rotation: %v", fixture.ctx.Err())
	}
	resume()
	select {
	case completed := <-outcome:
		if completed.err != nil || completed.result.Accepted != 1 {
			t.Fatalf("in-flight ingest before rotation = (%+v, %v)", completed.result, completed.err)
		}
	case <-fixture.ctx.Done():
		t.Fatalf("wait for in-flight ingest: %v", fixture.ctx.Err())
	}
	select {
	case err := <-rotationOutcome:
		if err != nil {
			t.Fatalf("rotation after ingest: %v", err)
		}
	case <-fixture.ctx.Done():
		t.Fatalf("wait for rotation: %v", fixture.ctx.Err())
	}
	events, facts := countCustomIngestRaceWrites(t, fixture)
	if events != 1 || facts != 1 {
		t.Fatalf("committed ingest before rotation = events=%d facts=%d, want one each", events, facts)
	}
}
