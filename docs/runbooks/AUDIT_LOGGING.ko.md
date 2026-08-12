# 감사 로그 운영 Runbook

기준일: 2026-08-13

이 runbook은 누가, 언제, 어떤 대상에, 무엇을 시도했고 결과가 무엇인지 비밀값 없이 재구성하는 절차를 정의합니다. Migration 0003의 `audit_events`, migration 0012/0013의 `mutation_audit_outbox`, CockroachDB sink, 재귀 redactor, 공통 HTTP mutation/거부 middleware와 maintenance audit wiring이 구현돼 있습니다. OpenAPI에 등록된 mutation과 OAuth callback은 shared IP/session pre-audit limit을 통과한 뒤 handler 실행 전에 `http.mutation.intent`를 영속화하며 실패하면 RFC 9457 503으로 fail closed합니다. 현재 production router의 24개 mutation route는 실제 chi route와 registry의 양방향 자동 검사를 통과하고, 성공 state와 redacted 최종 outcome을 같은 lazy Cockroach transaction에 commit한 뒤 worker가 append-only sink로 전달합니다. 2xx는 `succeeded`, 의도적으로 보안 상태를 소비하는 Passkey 오류는 `failed`/`denied`를 원자적으로 기록하며, state transaction이 시작되지 않은 오류는 direct audit 경로를 사용합니다. 실제 staging 검증과 아래 잔여 통제가 끝나기 전에는 전체 audit gate를 완료로 표시하지 않습니다.

## 1. 감사 이벤트 계약

모든 이벤트는 다음 필드를 필수로 갖습니다.

| 필드 | 규칙 |
| --- | --- |
| `id` | RFC 4122 UUID v4, 전역 유일 |
| `occurred_at` | server UTC 시각, DB 저장은 `TIMESTAMPTZ` |
| `actor.type` | `user`, `system`, `custom_provider`, `anonymous` 중 하나 |
| `actor.id` | 인증 actor의 내부 ID, anonymous는 `NULL` 허용. 이메일이나 credential 금지 |
| `action` | HTTP method와 고정 chi route pattern에서 유도한 최대 128자 동사 또는 maintenance 동사 |
| `target.type`, `target.id` | 고정 대상 종류와 route의 마지막 resource ID. 대상 ID가 없으면 `NULL` 허용 |
| `outcome` | `succeeded`, `denied`, `failed` |
| `request_id` | HTTP request ID 또는 background job correlation ID, 빈 값 금지 |
| `source_ip` | session key 기반 HMAC pseudonym이며 `metadata.source_ip`에 저장. 신뢰 proxy 정책 적용 |
| `metadata` | allowlist field만 포함하는 JSON object |

ID, 시각, actor type, action, target type, request ID는 필수입니다. Actor/target ID는 해당 request에 식별 대상이 없을 때만 비울 수 있습니다.

## 2. 현재 action 규칙과 목표 registry

Mutation intent는 raw URL/path/body를 저장하지 않고 allowlisted route가 정한 method, bounded target class, `phase=intent`, request ID만 기록합니다. Handler 뒤 outcome은 사용자 입력이 아니라 method와 고정 chi route pattern으로 구성합니다. 예를 들면 `post.v1.auth.magic-link.request`, `post.v1.subjects.subject.provider-connections`, `delete.v1.subjects.subject` 형태입니다. Intent를 통과한 인증·소유권 거부 및 endpoint 429는 기록하며, pre-audit cap 자체의 429는 storage 증폭을 피하려고 durable event를 만들지 않습니다. 별도 maintenance process는 `credential.reencrypt`, `retention.purge`를 기록합니다. 임의/unmatched POST·PUT·PATCH·DELETE도 intent/outcome 대상이 아닙니다.

아래 semantic registry는 향후 outbox/도메인 이벤트 전환 때 유지할 목표입니다. 현재 모든 항목이 별도 semantic action으로 구현됐다는 목록이 아닙니다.

- 인증: `auth.magic_link.request`, `auth.magic_link.consume`, `auth.passkey.register`, `auth.passkey.sign_in`, `auth.session.revoke`, `auth.sign_counter_regression`.
- authorization: `authorization.denied`.
- provider: `provider.connect`, `provider.token_refresh`, `provider.revoke`, `provider.sync.enqueue`, `provider.sync.fail`.
- custom provider: `custom_provider.create`, `custom_provider.update`, `custom_provider.delete`, `custom_provider.rotate_key`, `custom_provider.ingest.denied`.
- 사용자 데이터: `subject.create`, `subject.update`, `subject.delete`, `account.delete.request`, `account.delete.complete`.
- 운영: `key.rotate.start`, `key.rotate.complete`, `key.retire`, `migration.apply`, `migration.fail`, `backup.complete`, `restore.verify`, `deployment.promote`, `deployment.rollback`.

새 action은 owner, target type, 필수 metadata, 민감정보 검토와 함께 registry에 추가합니다. 사용자 입력을 action 문자열로 직접 사용하지 않습니다.

## 3. 저장 모델과 권한

현재 `audit_events` table의 핵심 schema는 다음과 같습니다.

```sql
CREATE TABLE audit_events (
  id UUID PRIMARY KEY,
  occurred_at TIMESTAMPTZ NOT NULL,
  actor_type STRING NOT NULL,
  actor_id STRING NULL,
  action STRING NOT NULL,
  target_type STRING NOT NULL,
  target_id STRING NULL,
  outcome STRING NOT NULL,
  request_id STRING NOT NULL,
  metadata JSONB NOT NULL DEFAULT '{}'::JSONB,
  CONSTRAINT audit_events_actor_type_chk
    CHECK (actor_type IN ('anonymous', 'user', 'system', 'custom_provider')),
  CONSTRAINT audit_events_outcome_chk
    CHECK (outcome IN ('succeeded', 'failed', 'denied'))
);
```

Migration에는 action/target/request 길이 제약과 `(occurred_at DESC)`, `(actor_type, actor_id, occurred_at DESC)`, `(target_type, target_id, occurred_at DESC)` index를 포함합니다. `request_id` 전용 index, DB 권한 기반 append-only 강제, schema version column은 아직 없습니다.

- `jandibat_api` role은 `audit_events`에 `INSERT`만 가능하고, `mutation_audit_outbox`에는 채택 adapter의 같은-transaction enqueue를 위한 `INSERT`만 가능합니다.
- Worker는 outbox `SELECT`/`UPDATE`와 sink `INSERT`만 가능하며 sink `SELECT`/`UPDATE`/`DELETE`와 outbox `INSERT`/`DELETE`는 거부됩니다. Sink INSERT와 delivered 전환은 같은 transaction이므로 부분 commit되지 않으며, 안정된 `audit_event_id` unique 충돌은 전달 실패로 fence되어 조사 대상이 됩니다.
- 보안 조회 role은 `SELECT`만 허용합니다. `jandibat_maintenance`는 audit 보존과 outbox terminal 보존을 위한 `SELECT`/`DELETE`, maintenance 결과 append를 위한 sink `INSERT`만 수행하고 outbox/sink `UPDATE`는 할 수 없습니다.
- API, sync worker, maintenance는 별도 process/container로 실행하고 각 process에는 자기 역할의 DSN만 주입합니다. 역할이 다른 DSN은 container 환경에 함께 넣지 않습니다.
- critical application mutation은 adapter별 registry에 등록해 같은 transaction outbox를 채택합니다. 0012 table/dispatcher의 존재만으로 미등록 adapter의 원자성을 주장하지 않습니다.
- backup 대상에 audit table과 mutation audit outbox를 포함합니다. Outbox pending/processing은 purge하지 않고 delivered/dead는 terminal 시각부터 400일 보존하며 active audit-event/subject/account legal hold를 제외한 뒤 maintenance만 삭제합니다.

## 4. 비밀값과 개인정보 금지

다음은 metadata, action, target, 오류 문구 어디에도 저장하지 않습니다.

- Authorization/Cookie header, session/magic-link/challenge 원문.
- OAuth access/refresh token, custom ingestion key, client secret, 암호화 key/ciphertext.
- WebAuthn assertion/clientDataJSON 원문.
- SMTP credential, external connection URI의 credential 부분.
- request/response body 전체, provider raw error body, stack trace.
- email, user-agent, 전체 IP. 필요하면 keyed pseudonym 또는 분류된 coarse value만 사용.

허용 metadata 예:

```json
{
  "provider": "github",
  "auth_method": "oauth2",
  "reason_code": "state_expired",
  "old_key_id": "cred-2026-08-a",
  "new_key_id": "cred-2026-11-a",
  "rows_processed": 500
}
```

redactor는 nested object/array와 URL query를 재귀적으로 처리합니다. redaction 실패 또는 지원하지 않는 metadata type은 원문 fallback log 없이 event를 거부합니다.

## 5. 현재 기록 실패 정책과 목표

현재 공통 HTTP middleware는 allowlisted mutation마다 Cockroach-shared `mutation_intent_ip`와 credential이 있으면 `mutation_intent_session` fixed-window bucket(각 120/minute)을 먼저 소비합니다. 초과하면 intent를 쓰지 않고 `Retry-After`와 RFC 9457 `429 rate_limited`를 반환합니다. 통과한 요청은 handler 전에 최대 2초 동안 write-ahead intent를 Cockroach sink에 동기식 append하며, 이 append가 실패하면 handler를 호출하지 않고 `Retry-After: 5`와 RFC 9457 `503 service_unavailable`을 반환합니다. Middleware는 성공 응답 body/header/cookie를 임시 buffer에 두고 state transaction에 outbox outcome을 enqueue·commit한 뒤에만 client에 내보냅니다. Enqueue/commit 실패나 handler deadline 초과는 state를 rollback하고 RFC 9457 503을 반환하므로 성공 state만 남거나 성공 응답만 먼저 노출되는 창이 없습니다. State가 없는 거부·실패는 direct sink에 기록하고, 의도적으로 ceremony를 소비한 Passkey 거부는 `denied`/`failed` outbox와 consumption을 함께 commit합니다. Maintenance worker는 실행 결과와 audit write 오류를 함께 실패로 취급합니다. 메모리 개발 모드는 memory sink를 사용합니다.

same-transaction outbox의 적용 범위와 예외는 다음과 같습니다.

- production router의 write route와 OAuth callback은 registry에 없거나 실제 route가 없으면 자동 테스트가 실패하며, adapter가 shared transaction에 참여하지 않으면 성공 응답을 commit할 수 없습니다.
- OAuth state consume은 provider network 중 DB transaction을 열어 두지 않고 replay를 먼저 차단하기 위해 의도적으로 별도 autocommit합니다. Callback의 최종 connection/ciphertext/outbox는 원자적이고 local commit 실패 시 fresh provider token revoke를 시도하지만, exchange 뒤 process crash를 완전히 닫으려면 provider-side durable revocation intent가 추가로 필요합니다.
- 로그인 실패·authorization denied처럼 state transaction이 시작되지 않은 이벤트는 direct durable sink에 동기식 기록합니다.
- 일반 public GET은 audit 대상이 아니며 audit 장애로 차단하지 않습니다.
- audit event를 일반 application log로 대체하지 않습니다.

목표는 critical mutation 유실 0건, 모든 audit event 영속화 지연 p99 60초 이하입니다.

## 6. 정상 운영 확인

매일 자동 확인:

- 최근 24시간 action/outcome별 count와 7일 baseline 편차.
- critical application mutation count와 outcome count의 일치, 5분 넘게 outcome이 없는 `http.mutation.intent` 0건.
- 빈 request ID/actor/target count가 0.
- audit persist failure, queue depth, oldest event age.
- 400일 cutoff보다 오래되고 legal hold가 없는 row count.
- metadata secret canary scan 결과 0건.

검증 명령:

```sh
AUDIT_DATABASE_URL="$STAGING_MAINTENANCE_DATABASE_URL" \
  scripts/verify-audit-log.sh --window 24h --orphan-age 5m \
  --output audit-reconciliation.json

go test ./apps/api/... -run 'Audit|Redact|Correlation|CriticalMutation'
```

Verifier는 bounded window의 HTTP intent/outcome orphan·duplicate, 완료 deletion과 `deletion.completed` event의 양방향 ID/request/backup-expiry 일치, 필수 field와 credential-shaped canary를 검사합니다. 또한 delivered outbox와 sink payload의 전 필드 일치, 오래된 pending/lease-expired processing, dead job 0건을 검사합니다. JSON artifact를 만들고 위반이 하나라도 있으면 non-zero로 종료합니다. 이는 same-transaction outbox 전달과 direct failure audit을 함께 조정하며 실제 staging artifact가 없으면 해당 항목은 `NOT RUN`입니다.

## 7. 조사 절차

1. ticket에 incident ID, UTC 범위, request ID 또는 대상 pseudonym만 기록.
2. read-only security role로 action/outcome/time 범위를 먼저 좁힘.
3. export가 필요하면 암호화된 제한 버킷에 최소 column만 저장하고 7일 expiry 설정.
4. query와 export 자체를 `audit.export` event로 기록.
5. 실제 식별자와 pseudonym 연결은 별도 승인된 도구 안에서만 수행.
6. 조사 종료 시 export 삭제와 access log를 확인.

비밀 유출이 의심되면 조회를 중단하고 key/token rotation runbook과 incident 절차를 시작합니다.

## 8. 이상 및 복구

다음은 security incident입니다.

- critical mutation에 대응하는 audit event 누락.
- audit row UPDATE/DELETE 또는 checksum/append-only invariant 위반.
- token/key/email/raw IP 검출.
- occurred time 역전 또는 5분 초과 clock skew.
- unauthorized audit 조회/export.

대응:

1. 영향 release와 writer를 격리하고 신규 critical mutation을 차단.
2. DB, outbox, immutable backup으로 유실 범위를 계산.
3. 동일 request ID로 안전하게 reconstruct 가능한 event만 보완하고 `audit.recovered` 표시.
4. 원본 증거를 덮어쓰거나 기존 row를 수정하지 않음.
5. root cause, 유실 범위, 재발 방지와 사용자/규제 통지 판단을 incident 문서에 기록.

## 9. 자동 테스트와 staging 인수

PR gate:

- 필수 field와 enum validation.
- nested secret/url/header redaction.
- critical mutation과 outbox 원자성.
- DB role이 UPDATE/DELETE/TRUNCATE를 거부하는지 확인.
- retry가 duplicate event를 만들지 않는지 확인.
- queue saturation/fail-closed와 context cancellation.
- 400일 retention boundary와 legal hold.

staging 인수:

1. passkey 실패, 소유권 거부, provider 연결/revoke, ingestion key rotation, subject 삭제를 synthetic tenant에서 실행.
2. 각 요청의 `X-Request-ID`로 정확히 한 event를 조회.
3. secret canary가 어떤 field에도 나타나지 않음을 확인.
4. audit sink를 2분 차단하고 critical mutation rollback과 recovery를 확인.
5. 24시간 count reconciliation 결과와 dashboard 링크를 저장.

실행일, commit/image digest, migration checksum, raw test artifact, pass/fail을 `docs/evidence/staging/`에 남깁니다. 실행하지 않은 단계는 `NOT RUN`입니다.
