# Runtime DB 역할 Runbook

기준일: 2026-09-24

Production은 로그인 가능한 CockroachDB 사용자 `jandibat_migrator`, `jandibat_api`, `jandibat_worker`, `jandibat_maintenance`와 별도 backup 사용자를 사용합니다. 세 애플리케이션 process는 자기 DSN username을 고정값과 비교하며, 다른 역할의 DSN은 환경에 주입하지 않습니다.

## 프로비저닝

1. Secure CockroachDB에서 chart가 생성한 root client certificate가 포함된 `COCKROACH_ROOT_URL`, 독립된 SOPS 키에서 가져온 `JANDIBAT_MIGRATOR_PASSWORD`, `JANDIBAT_API_PASSWORD`, `JANDIBAT_WORKER_PASSWORD`, `JANDIBAT_MAINTENANCE_PASSWORD`, 그리고 각 역할의 `MIGRATION_DATABASE_URL`, `API_DATABASE_URL`, `WORKER_DATABASE_URL`, `MAINTENANCE_DATABASE_URL`을 bootstrap Job 환경에 제공합니다. Password는 서로 달라야 하며 unpadded base64url 43자 이상이어야 합니다. `make db-bootstrap-roles`는 `jandibat` 데이터베이스와 네 LOGIN 사용자를 멱등 생성하고 password를 갱신한 뒤 각각의 DSN으로 로그인을 확인합니다. Bootstrap은 migrator에게 이 DB의 `CONNECT`와 `public` schema의 `USAGE`·`CREATE`에만 재부여 권한을 줍니다. Runtime table 권한은 부여하지 않으며 기존 schema object를 변경하거나 삭제하지 않습니다. Credential을 shell history, repository, CI artifact에 기록하지 않습니다.
2. Bootstrap 성공 후 전용 migration Job에 `MIGRATION_DATABASE_URL`, `COCKROACH_DATABASE`, `MIGRATIONS_DIR`, Cockroach CLI, writable `/tmp`, 읽기 전용 SQL/script와 DB TLS CA를 제공해 모든 migration을 적용합니다. URL의 database와 `COCKROACH_DATABASE`가 일치해야 합니다.
3. 같은 migration credential로 `scripts/db-configure-runtime-roles.sh`를 실행합니다. Script는 세 사용자가 존재하고 `LOGIN` 가능한지 확인한 뒤 기존 runtime table 권한을 모두 회수하고 allowlist를 다시 부여합니다. Role negative check까지 통과해야 application rollout을 시작합니다.
4. API, worker, maintenance container에는 각각 `DATABASE_URL`, `WORKER_DATABASE_URL`, `MAINTENANCE_DATABASE_URL` 하나만 주입합니다. Migrator DSN은 어떤 application workload에도 주입하지 않습니다.

```sh
MIGRATION_DATABASE_URL="$STAGING_MIGRATION_DATABASE_URL" \
  COCKROACH_DATABASE=jandibat \
  make db-configure-runtime-roles
```

Staging 배포 script는 migration 직후 이 GRANT script를 다시 실행하므로 새 table이 생긴 release에서 명시적 권한 갱신이 빠지면 runtime readiness/smoke가 실패합니다. Script는 `public`의 암묵적 schema `CREATE`를 회수하고 `jandibat_migrator`에만 명시적으로 유지합니다. Runtime 사용자에는 schema `CREATE` 권한을 주지 않습니다.

Job은 release에 고정된 SQL/script를 사용합니다. 재실행 시 적용된 version의 checksum이 같으면 건너뛰며 다르면 중단합니다. Migration이나 GRANT 실패 시 세 runtime workload를 시작하지 않습니다. 정확한 image/port/probe/secret mount 계약은 [`IMAGE_RUNTIME_CONTRACT.ko.md`](../IMAGE_RUNTIME_CONTRACT.ko.md)를 따릅니다.

## 권한 행렬

| 역할 | SELECT | INSERT | UPDATE | DELETE |
| --- | --- | --- | --- | --- |
| `jandibat_api` | 사용자/subject/auth/activity/provider/custom/idempotency/rate-limit runtime table와 mail outbox | 같은 runtime write table + `audit_events` + mutation audit outbox + encrypted provider revoke queue + mail delivery intent enqueue | mutable runtime table + mail intent supersede/consume + 기존 magic token invalidate | passkey/custom/fact/cache/job의 명시적 삭제 경로. Subject/account 삭제는 inbox enqueue만 |
| `jandibat_worker` | provider connection/sync job/revoke queue, subject settings, environment/fact, 비밀 열이 없는 `custom_providers`, FK-free mail outbox, mutation audit outbox | sync job, environment, fact, `audit_events` | provider connection의 sync 상태 열, sync/revoke/mail lease와 mail hash activation, mutation audit claim/delivery 상태 | sync range의 fact와 완료된 revoke queue row. mutation audit outbox INSERT/DELETE와 audit sink read/update/delete 없음 |
| `jandibat_maintenance` | provider/credential scan, purge 대상(terminal mail/audit outbox 포함), checkpoint/legal-hold/deletion 상태 | `audit_events`, checkpoint/deletion state, account-delete 중 encrypted revoke queue | provider/revoke ciphertext CAS, checkpoint/deletion state, user deletion 상태 | allowlist retention 및 deletion workflow 파생 데이터와 terminal mutation audit outbox |

`jandibat_migrator`는 migration이 소유한 schema object를 생성·변경하는 전용 사용자이며 runtime container에 주입하지 않습니다. Runtime table CRUD 행렬의 일부가 아닙니다.

중요한 negative boundary:

- API는 `audit_events`를 읽거나 수정·삭제하지 못합니다.
- API는 provider revoke queue에 encrypted job을 넣을 수 있지만 읽거나 갱신·삭제하지 못합니다.
- API는 owner-authorized custom provider aggregate에서 provider-owned custom event/fact/cache를 삭제합니다. Cockroach FK cascade에 필요한 `custom_activity_events` DELETE는 허용하지만 `environments` DELETE 권한은 없습니다. Canonical `connection:%`/`custom-provider:%` orphan cleanup은 maintenance의 all-reference-empty, legal-hold-aware bounded retention query만 수행합니다.
- CockroachDB는 outbox `UPDATE ... WHERE ...`/`RETURNING`에도 table SELECT를 요구하고 column-level GRANT를 지원하지 않으므로 API mail outbox SELECT는 허용합니다. API process에는 SMTP가 없고 outbox에는 raw/decryptable token이 없으며 외부 HTTP read 경로도 제공하지 않습니다.
- Worker는 user/session/passkey/challenge, legacy `magic_link_tokens`, custom ingestion, rate-limit, retention table에 접근하지 못합니다. Magic Link delivery에는 FK가 없는 intent outbox의 SELECT/UPDATE만 허용합니다. Audit dispatcher에는 `mutation_audit_outbox` SELECT/UPDATE와 `audit_events` INSERT만 추가하며 sink read/update/delete는 거부합니다.
- CockroachDB의 `activity_facts.custom_provider_id` FK 검사는 worker의 fact INSERT에도 참조 테이블 SELECT를 요구합니다. `custom_providers`에는 provider ID와 비밀이 아닌 설정만 두고, SHA-256 ingest token digest와 key ID는 `custom_provider_secrets`로 분리합니다. Worker는 기본 테이블 SELECT만 허용하며 secret 테이블 SELECT는 negative test로 거부합니다. API의 provider 저장은 두 테이블을 한 transaction에서 갱신합니다.
- Worker는 provider revoke queue를 claim/update/delete할 수 있지만 새 queue row를 만들지 못합니다.
- Worker adapter는 provider connection에서 `sync_cursor`, `status`, `last_synced_at`, `last_error`, `updated_at`만 갱신하고 새 connection을 만들지 않습니다. CockroachDB v26.2는 column-level privilege를 지원하지 않으므로 DB 역할의 `UPDATE`는 table 단위이며, 이 열 제한은 process 분리와 고정된 CAS SQL로 강제합니다.
- Maintenance는 사용자/auth/provider 연결을 새로 만들지 못합니다. `legal_holds`는 읽기만 가능하고 추가·삭제는 별도 승인된 관리 identity가 수행합니다.
- Maintenance는 평상시 revoke queue의 ciphertext/key ID만 CAS re-encrypt하고 삭제하지 못합니다. Account deletion transaction은 provider connection에서 제거한 encrypted access token을 같은 queue에 보존해야 하므로 queue `INSERT`는 허용합니다.
- Maintenance deletion workflow는 user를 `deletion_pending`으로 전환한 뒤 고정된 SQL로 owned subject/auth/provider/activity 데이터를 삭제합니다. Orphan environment purge는 trusted namespace, subject ownership, 여섯 reference 부재와 legal hold를 모두 검사합니다. Table-level `UPDATE`/`DELETE`보다 좁은 상태·열·순서는 전용 process와 adapter SQL, durable `deletion_requests` stage로 강제합니다.
- 어떤 runtime 역할도 `schema_migrations`를 읽거나 migration/schema DDL을 실행하지 못합니다.

정확한 table allowlist는 executable source인 `scripts/db-configure-runtime-roles.sh`가 기준입니다. 새 adapter가 새 table/operation을 필요로 하면 최소 권한을 검토하고 script, 이 문서, staging 권한 negative test를 같은 변경에 포함합니다.

## 검증과 회수

Staging에서는 각 DSN으로 정상 `/readyz`를 확인한 뒤 `scripts/db-verify-runtime-roles.sh`로 다음 허용/거부를 검사합니다. Local CockroachDB/CI의 `make db-runtime-roles-test`는 insecure cluster에서 root로 GRANT를 재적용하는 기존 회귀 검사입니다. Migrator의 실제 권한 경계는 x86_64 Linux의 격리된 secure fixture인 `sh scripts/test-db-bootstrap-roles-secure.sh`에서 bootstrap → migrator migration 두 번 → migrator GRANT → role별 허용/거부 검증으로 확인합니다.

- API role의 `SELECT`/`DELETE FROM audit_events` 거부.
- API role의 revoke queue `INSERT` 허용과 `SELECT` 거부.
- API role의 custom provider/custom event aggregate DELETE 허용과 `DELETE FROM environments` 거부. 타 owner route는 HTTP authorization test로 거부.
- Worker role의 revoke queue `SELECT`/`DELETE` 허용과 `INSERT` 거부, mail outbox SELECT/UPDATE 허용. Mutation audit outbox SELECT/UPDATE 및 audit sink INSERT는 허용하고 outbox INSERT/DELETE와 sink SELECT/UPDATE/DELETE는 거부.
- Worker role의 `custom_providers` ID SELECT 허용과 `custom_provider_secrets.ingest_token_hash` SELECT 거부. API role의 secret 테이블 읽기/쓰기 허용.
- Maintenance role의 checkpoint/deletion state CRUD, legal-hold SELECT, revoke queue `SELECT`/ciphertext `UPDATE`/account-delete enqueue 허용과 queue `DELETE` 거부.
- Maintenance role의 user/subject empty-set delete 허용, `INSERT INTO user_sessions`, `INSERT/DELETE legal_holds`, `UPDATE audit_events` 거부.
- 세 역할의 `CREATE TABLE` 거부.

퇴역 credential은 새 credential 배포와 readiness 확인 뒤 즉시 revoke합니다. Permission 변경과 credential revoke 시각, migration SHA, image digest, negative test 결과를 `docs/evidence/staging/`에 남깁니다. 현재 실제 staging 증거는 없으므로 production promotion gate는 아직 통과하지 않았습니다.
