# 데이터 보존 및 삭제 정책

기준일: 2026-08-13

이 문서는 서비스 데이터의 기본 보존 기간과 삭제 절차를 정의합니다. 별도 CockroachDB maintenance process는 시작 직후와 기본 24시간마다 bounded purge를 수행하도록 배선돼 있습니다. API와 sync worker에는 purge 권한이나 retention loop가 없습니다. Domain/application과 Cockroach adapter에는 side-effect-free retention dry run, 영속 checkpoint/resume, legal-hold 제외, 영속 계정/subject 삭제 workflow가 구현돼 있고 operator CLI와 검증 wrapper도 제공됩니다. 실제 staging 삭제 실행 증거는 아직 없으므로 전체 Phase 3 gate를 완료로 주장하지 않습니다.

## 1. 원칙

- 수집 목적에 필요한 최소 데이터만 저장합니다.
- 비밀 원문은 응답·로그·감사 metadata·fixture에 저장하지 않습니다.
- `expires_at`은 접근 거부 시점이고, 물리 삭제는 아래 purge SLA 안에 수행합니다.
- 사용자 삭제는 primary DB에서 7일 안에 완료하고, backup 사본은 선택 수정하지 않고 35일 보존창이 끝날 때 만료시킵니다.
- 삭제된 계정 재생성 차단에는 raw email이나 dictionary-enumerable SHA-256를 신규 저장하지 않고, 별도 versioned key의 HMAC-SHA-256 digest만 `deleted_identity_tombstones_v2`에 저장합니다. 기존 v1 SHA row는 raw email 부재로 backfill할 수 없어 최대 retention window 동안 read-only transition 후 폐기합니다.
- 삭제와 purge는 idempotent하고 batch 단위로 재시작 가능해야 합니다.
- legal hold는 문서화된 승인과 만료일이 있을 때만 적용합니다. 활성 hold가 있으면 사용자에게 허용되는 범위에서 삭제 지연을 기록합니다.
- 운영자가 임의 SQL로 삭제하지 않고 application deletion workflow 또는 승인된 purge job을 사용합니다.

## 2. 데이터 분류와 기간

| 데이터/현재 테이블 | 분류 | 활성 보존 | 비활성·만료 후 물리 삭제 | 삭제 시 처리 |
| --- | --- | --- | --- | --- |
| `users` email/profile | 개인 식별 | 계정 활성 기간 | 계정 삭제 승인 후 7일 | 계정과 owned resource 삭제, 감사 증거에는 원 ID 대신 keyed pseudonym 사용 |
| `subjects`, settings | 사용자 콘텐츠/설정 | 계정 또는 subject 활성 기간 | subject 삭제 후 24시간 | 연관 environment, fact, cache, connection을 함께 삭제 |
| `user_passkeys` | 인증 공개키/식별자 | 등록 기간 | credential 삭제 또는 계정 삭제 후 24시간 | credential ID, public key, label 삭제 |
| `magic_link_tokens` | 인증 민감 | 발급 후 만료/소비까지 | `max(expires_at, consumed_at)` 이후 24시간 | token hash와 email 삭제 |
| `magic_link_mail_outbox` | 인증 민감/메일 주소/redirect intent | `pending`/`processing`은 전송 또는 최대 5회 시도까지, `sent`/`dead`/`superseded`는 최대 24시간 | terminal 전환 후 24시간 안에 bounded purge | API는 token 없는 intent enqueue/supersede만, worker는 claim 후 일회성 token을 메모리에서 만들고 hash만 활성화한 뒤 전송. raw/decryptable token은 DB에 저장하지 않고 API에는 SMTP를 주입하지 않음 |
| `user_sessions` | 인증 민감 | 만료 또는 revoke까지 | 만료/revoke 후 30일 | token hash, IP, user-agent 삭제; 보안 통계는 비식별 집계만 유지 |
| `auth_challenges` | 인증 민감 | ceremony 만료/소비까지 | 만료/소비 후 24시간 | challenge hash와 payload 삭제 |
| `provider_connections` credentials | 비밀/연동 | 연결 활성 기간 | disconnect 성공 트랜잭션에서 connection ciphertext를 즉시 `NULL`; metadata row는 30일과 revoke queue 완료 중 늦은 시점 | OAuth ciphertext는 같은 transaction의 worker-only revoke queue로 옮겨 provider revoke를 재시도. Subject/account cascade 시 nullable FK만 끊고 queue는 완료까지 보존 |
| `custom_providers` ingestion key | 비밀/연동 | provider 활성 기간 | rotate 시 구 hash 즉시 무효화, 삭제 시 hash 즉시 제거 | 평문은 create/rotate 응답에서 한 번만 반환, 연관 custom activity 삭제 범위를 먼저 계산 |
| `activity_facts` | 사용자 활동 | activity date 이후 400일 | 매일 purge, 최대 24시간 지연 | subject/account 삭제 시 기간과 무관하게 삭제 |
| `timeline_cache`, `activity_refresh_cache` | 파생 데이터 | 유효 기간 | expiry 후 7일, subject 삭제 시 즉시 | 원본보다 오래 유지하지 않음 |
| `provider_sync_jobs` | 운영 metadata | 성공 30일, 실패/취소 90일 | 매일 purge | `last_error`는 credential/provider raw body를 포함하지 않음 |
| `audit_events` | 보안 기록 | 400일 | 기본 24시간 purge 주기 | `legal_holds`의 미만료 account/subject/audit-event 대상은 retention query에서 제외 |
| `mutation_audit_outbox` | 보안 기록/전달 상태 | `pending`/`processing`은 purge 금지, `delivered`/`dead`는 terminal 시각부터 400일 | `COALESCE(delivered_at, terminal_at)` cutoff의 bounded purge | redacted `succeeded`/intentional `failed`/`denied` outcome만 저장. active audit-event/subject/account legal hold는 제외하고, delivered sink 전 필드 일치를 검증하며 dead/stale은 release gate 실패로 처리 |
| application logs/traces | 운영 telemetry | 30일 | backend lifecycle로 자동 만료 | email/token/request body 금지, user ID는 keyed pseudonym |
| database backups | 재해 복구 사본 | 35일 | object lifecycle로 만료 | 개별 행 삭제 대신 접근 통제 후 보존창 만료; 복원 시 deletion replay 필요 |
| `deleted_identity_tombstones` (legacy v1) | 개인 식별 파생값/dictionary-enumerable SHA-256 | v2 전환 중 기존 row만 최대 35일 read | 마지막 v1 `expires_at` 이후 role 회수와 table 제거 migration | 신규 write 금지. API는 transition 동안 dual-read만, maintenance는 만료 purge만 수행 |
| `deleted_identity_tombstones_v2` | 개인 식별 파생값/versioned HMAC-SHA-256 | 삭제 완료 후 35일 또는 더 긴 backup expiry까지 | `expires_at` 이후 bounded purge | maintenance가 active key ID로 write, API는 보존 중인 모든 key version으로 조회. raw email과 HMAC key는 저장하지 않음 |
| CI/staging evidence | release 증거 | 400일 | artifact lifecycle로 만료 | secret, 실제 사용자 데이터 금지 |

400일 activity 보존은 1년 heatmap과 시간대 경계 재집계를 위한 35일 여유를 포함합니다. 제품 요구가 바뀌면 OpenAPI/계획/이 문서를 함께 갱신합니다.

## 3. 삭제 요청 상태 모델

`operations.DeletionWorkflow`와 Cockroach adapter는 다음 상태를 `deletion_requests`에 영속적으로 기록합니다. 동일 대상의 미완료 요청은 partial unique index로 하나만 허용합니다.

1. `requested`: 요청자 재인증, 대상, UTC 시각, request ID 기록.
2. `revoking`: session, magic link, OAuth/provider token, ingestion key 접근 차단.
3. `deleting_primary`: owned subject와 연관 데이터를 명시적으로 삭제.
4. `verifying`: 대상별 count와 object storage 파생물 잔존 여부 확인.
5. `completed`: primary DB 삭제 시각, backup expiry deadline, 감사 event ID 기록.
6. `failed`: 마지막 성공 단계와 안정된 오류 코드 기록; worker가 같은 request ID로 재시도.

동일 대상에 두 요청이 들어오면 기존 미완료 request를 반환합니다. 완료된 요청의 재실행은 no-op입니다.

## 4. 계정/subject 삭제 순서

현재 `subjects.owner_user_id`가 `ON DELETE SET NULL`이므로 user row를 먼저 삭제하면 owned data가 고아가 될 수 있습니다. 구현된 deletion workflow는 다음 순서를 durable stage로 보장합니다.

1. 대상 user를 `deletion_pending`으로 전환하고 신규 login/mutation을 차단.
2. 모든 session과 미사용 magic link/challenge를 revoke.
3. provider 측 revoke를 시도하고 결과와 retry deadline을 기록. 외부 revoke 실패가 local credential 삭제를 지연시키면 안 됨.
4. 대상 user가 소유한 subject ID 목록을 고정.
5. 각 subject의 custom provider, sync job, fact, cache, environment, provider connection을 삭제.
6. subject를 삭제한 후 passkey와 user를 삭제.
7. 다음 검증 query가 모두 0인지 확인하고 삭제 완료 감사 이벤트 기록.

검증 query는 운영 도구가 parameter binding으로 실행해야 하며 로그에는 실제 ID를 출력하지 않습니다.

```sql
SELECT count(*) FROM subjects WHERE owner_user_id = $1;
SELECT count(*) FROM user_passkeys WHERE user_id = $1;
SELECT count(*) FROM user_sessions WHERE user_id = $1;
SELECT count(*) FROM magic_link_tokens WHERE user_id = $1 OR email = $2;
SELECT count(*) FROM provider_connections WHERE subject_id = ANY($3);
SELECT count(*) FROM custom_providers WHERE subject_id = ANY($3);
SELECT count(*) FROM activity_facts WHERE subject_id = ANY($3);
SELECT count(*) FROM timeline_cache WHERE subject_id = ANY($3);
SELECT count(*) FROM activity_refresh_cache WHERE subject_id = ANY($3);
SELECT count(*) FROM provider_sync_jobs WHERE subject_id = ANY($3);
```

CockroachDB의 실제 parameter/array 문법은 구현 시 integration test로 고정합니다. 수동으로 ID를 SQL 문자열에 보간하지 않습니다.

## 5. Custom provider 삭제

Production Cockroach adapter는 custom provider 삭제 전에 `(provider_id, subject_id, environment_id)`를 row lock으로 고정하고 다음을 한 transaction에서 처리합니다.

- ingestion key 무효화.
- 진행 중/queued sync·ingest 작업 중단.
- `custom_provider_id`로 연결된 projection fact 삭제와 provider row 삭제. Ledger/idempotency row와 이미 저장된 fact는 FK cascade도 보조 경계로 사용.
- 해당 custom environment에서 파생된 facts와 caches 삭제.
- API role에는 `environments` DELETE를 주지 않습니다. Provider transaction은 environment를 즉시 삭제하지 않고 maintenance orphan cleanup에 넘깁니다.
- 삭제 전후 count와 감사 event ID 기록.

새 custom environment ID는 사용자 slug가 아니라 `custom-provider:<provider UUID>` namespace를 사용하므로 tenant 사이에서 공유되지 않습니다. In-flight ingest가 삭제 뒤 fact를 쓰려고 하면 `activity_facts.custom_provider_id` FK가 실패하고, stale PATCH/rotate는 UPDATE-only CAS라 삭제된 provider를 재생성하지 못합니다. Maintenance는 보존 지연 없이 `connection:%`/`custom-provider:%` subject-scoped environment만 대상으로 삼고 provider connection/sync job/custom provider/custom event/fact/refresh cache 여섯 참조가 모두 없을 때만 bounded delete합니다. `custom:%` legacy/public namespace, global environment, active subject legal hold는 삭제하지 않습니다. Memory development adapter도 같은 결과를 회귀 테스트로 고정하지만 production 원자성 근거는 Cockroach transaction입니다.

UI 문구와 API contract는 “향후 수집 중단”인지 “기존 활동 포함 완전 삭제”인지 명확히 구분해야 합니다.

## 6. 주기 purge

현재 `/bin/maintenance` CockroachDB process는 `MAINTENANCE_DATABASE_URL`의 전용 role로 다음 동작을 제공합니다.

- 시작 직후와 `RETENTION_INTERVAL`(기본 `24h`)마다 실행.
- 한 실행의 UTC `now`를 고정하고 dataset별 retention 기간을 빼 동일 cutoff를 모든 batch에 사용.
- `MAINTENANCE_BATCH_SIZE`(기본 500) 단위, dataset별 `RETENTION_MAX_BATCHES`(기본 30)로 상한 적용.
- 전체 run을 `MAINTENANCE_TIMEOUT`(기본 15분)으로 제한.
- dataset 하나가 실패해도 나머지를 처리하고 deleted/failed/truncated 요약을 `retention.purge` audit metadata에 기록.
- audit, facts, custom events, 완료 sync jobs, session, magic link, auth challenge, ingest idempotency, caches, rate-limit bucket을 각 timestamp 기준으로 삭제.
- Revoked provider tombstone은 30일이 지나고 연결된 revoke queue가 없을 때 삭제합니다. Subject/account 삭제가 먼저 일어나면 미완료 encrypted revoke queue의 nullable connection FK는 `ON DELETE SET NULL`로 끊기지만 token job은 보존되며, worker가 provider revoke를 완료한 뒤 삭제합니다. 따라서 subject/account 삭제도 외부 revoke retry 때문에 실패하지 않습니다.
- SQL은 고정 table/column registry와 parameterized cutoff/limit만 사용하며 row의 민감값을 출력하지 않음.

Operations layer는 dry-run count, `maintenance_checkpoints` 기반 resume, legal-hold 제외를 구현합니다. 주기 background loop는 기존 bounded run을 사용하고, `/bin/maintenance retention`과 `scripts/purge-expired-data.sh`가 같은 영속 operator 경로를 호출합니다.

실행 인터페이스:

```sh
MAINTENANCE_DATABASE_URL="$STAGING_MAINTENANCE_DATABASE_URL" \
  scripts/purge-expired-data.sh --as-of '2026-08-12T00:00:00Z' --dry-run --scope staging-retention

MAINTENANCE_DATABASE_URL="$STAGING_MAINTENANCE_DATABASE_URL" \
  scripts/purge-expired-data.sh --as-of '2026-08-12T00:00:00Z' --execute --scope staging-retention --resume
```

Wrapper는 `MAINTENANCE_BIN`(기본 `/bin/maintenance`)에 검증된 argument만 전달합니다. 명령은 구현됐지만 staging 결과가 남기 전까지 운영 완료 증거는 아니며, adapter unit test를 실제 operator 실행으로 가장해 기록하지 않습니다.

## 7. Backup에서의 삭제

- immutable backup을 행 단위로 수정하지 않습니다.
- 삭제 완료 시 `backup_expiry_at = completed_at + 35일`을 기록합니다.
- restore는 production과 격리된 새 database/cluster로 수행합니다.
- 복원 직후 backup 생성 이후 완료된 deletion request를 replay한 뒤에만 사용자 트래픽을 허용합니다.
- replay 결과에서 잔존 row가 하나라도 있으면 cutover를 금지합니다.
- backup bucket lifecycle과 실제 oldest object가 35일 정책을 만족하는지 매일 검사합니다.

## 8. 자동 검증 gate

PR DB integration test:

- 각 table에 보존 경계 바로 전/정확히 경계/바로 후 fixture를 생성.
- dry-run이 count만 반환하고 row를 바꾸지 않는지 확인.
- execute가 만료 row만 삭제하는지 확인.
- batch 중 process를 종료한 뒤 재실행해 중복/누락이 없는지 확인.
- user 삭제 뒤 10개 검증 query가 모두 0인지 확인.
- custom provider 삭제가 다른 environment fact를 보존하는지 확인.
- API custom-provider DELETE가 environment 권한 없이 204와 succeeded outbox를 남기고, maintenance가 canonical orphan만 삭제하며 legacy/global/held/referenced environment를 보존하는지 실제 역할로 확인.
- legal hold와 backup expiry deadline을 확인.

staging gate:

```sh
scripts/purge-expired-data.sh --as-of "$FIXED_AS_OF" --dry-run
scripts/purge-expired-data.sh --as-of "$FIXED_AS_OF" --execute
scripts/verify-deletion.sh --request-id "$DELETION_REQUEST_ID"
```

세 명령의 exit code, row-count summary, 실행 전후 migration SHA를 `docs/evidence/staging/`에 기록합니다. 실행하지 않은 명령은 `NOT RUN`으로 표시합니다.

## 9. 예외와 변경 승인

보존 기간 연장은 data owner와 security owner의 승인이 필요하고 다음을 기록합니다.

- 대상 데이터와 목적.
- 현재/변경 기간.
- 법적 또는 제품 근거.
- 접근 가능한 role.
- 만료일과 삭제 확인 담당자.
- risk/issue 링크.

기간 단축은 API/사용자 기대, backup/RPO, 감사 요구에 미치는 영향을 검토한 뒤 변경합니다.

Identity HMAC key 회전에서는 maintenance write의 active ID만 먼저 바꾸고 API가 `DELETED_IDENTITY_HMAC_KEYS`의 구 key를 계속 읽게 합니다. 구 key는 해당 version의 마지막 tombstone `expires_at`과 그 row가 포함될 수 있는 backup expiry 중 더 늦은 시점까지 유지합니다. 최소 35일 전환 창과 restore/deletion replay 검증이 끝난 뒤에만 구 key와 legacy v1 SELECT grant/table을 별도 additive migration으로 제거합니다.
