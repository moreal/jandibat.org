# Token 및 Key Rotation Runbook

기준일: 2026-08-13

이 runbook은 session 설정 secret, credential encryption key, provider OAuth token, custom provider ingestion key, backup external connection credential을 교체하는 절차를 정의합니다. 현재 runtime은 active RSA public/private keyring, legacy raw/v1/v2 read, random per-record DEK를 RSA-OAEP-SHA-256으로 감싼 `JDBK` v3 envelope write, 주기적 CAS re-encryption과 key별 decrypt metric을 사용합니다. Internet-facing API에는 public key만 있고 worker/maintenance만 private key를 받습니다. Operations layer의 persistent checkpoint/resume와 operator CLI/wrapper는 구현됐고 external KMS/HSM은 아직 없습니다. Staging 실행 기록이 생기기 전에는 검증 완료로 간주하지 않습니다.

## 1. 역할과 승인

- Incident Commander 또는 Release Owner: 시작/중단/rollback 결정.
- Security Owner: 새 key 생성, secret manager version 활성화와 구 key 폐기 승인.
- Platform Operator: 배포와 batch re-encryption 실행.
- Database Operator: migration/checksum과 ciphertext key ID count 검증.
- Observer: dashboard, audit event, 증거 기록. 비밀값에는 접근하지 않음.

정기 회전은 credential encryption key 90일, session 설정 secret 90일, backup external connection credential 90일을 최대 주기로 합니다. 현재 `SESSION_SIGNING_KEY` 교체는 session token 회전이 아니라 필수 configuration secret 교체라는 점을 5절에 따라 기록합니다. 유출 의심, 접근자 변경, 암호화 정책 변경, provider 요구가 있으면 즉시 emergency rotation을 수행합니다.

## 2. 현재 지원 상태와 실행 gate

| 대상 | 현재 동작 | 지금 회전 가능 여부 |
| --- | --- | --- |
| `SESSION_SIGNING_KEY` | production 시작/secret 분리 검사에만 사용. Session은 opaque 난수 token의 SHA-256 digest로 검증 | 이 값의 교체는 session rotation이 아님. 기존 session도 무효화하지 않음 |
| Provider connection access/refresh token | 각 write마다 random 256-bit DEK로 AES-GCM 암호화하고 active RSA public key로 감싼 authenticated `JDBK` v3 envelope로 저장. API는 encrypt-only, worker/maintenance는 matching private key로 read | dual-read 회전 가능 |
| Legacy provider ciphertext | Optional symmetric map/single key로 raw/v1/v2 read | maintenance process가 active v3 envelope로 전환할 때까지 worker/maintenance에만 유지 |
| `operations.ReencryptionWorker` | 시작 직후와 기본 1시간마다 cursor scan, CAS replace, corrupt row 격리; 별도 `RunOperator`는 persistent checkpoint/resume | background와 `/jandibat-maintenance reencrypt` operator CLI 연결됨 |
| Custom provider ingestion key | 32-byte 난수의 base64url 평문을 한 번 반환하고 SHA-256 digest만 저장 | owner rotate endpoint로 즉시 교체 가능. credential cipher와 무관 |
| Backup external connection credential | CockroachDB external connection이 소유 | DB 권한과 storage credential overlap이 있으면 별도 회전 가능 |

다음이 모두 준비되기 전에는 production credential active key를 전환하지 않습니다.

- `CREDENTIAL_ENCRYPTION_PUBLIC_KEYS`와 `CREDENTIAL_ENCRYPTION_PRIVATE_KEYS`가 구/new RSA pair를 모두 포함하고 secret manager version/rollback reference가 고정됨.
- `CREDENTIAL_ACTIVE_KEY_ID`가 두 map의 matching RSA-2048 이상 entry를 정확히 선택함.
- V1/v2/raw row가 있으면 32-byte symmetric `CREDENTIAL_ENCRYPTION_KEYS`/`CREDENTIAL_ENCRYPTION_KEY`를 worker/maintenance만 유지함.
- migration 0003~0005를 포함한 현재 migration 전체가 checksum 일치 상태로 적용됨. 0003의 token key-ID column과 `audit_events`, 0004의 `api_rate_limit_buckets`, 0005의 encrypted provider revoke queue가 없으면 해당 process readiness/startup이 실패함.
- `credential.reencrypt` audit event와 `provider_connections`의 key-ID count를 조회할 수 있음.
- 대상은 `provider_connections.access_token_ciphertext`/`refresh_token_ciphertext`와 미완료 `provider_token_revocation_jobs.token_ciphertext`임. `custom_providers.ingest_token_hash`는 one-way digest이고 maintenance inventory에서도 제외됨.
- 최근 24시간 안의 backup과 격리 restore evidence가 있음.
- Staging canary에서 아래 중단/rollback 기준을 실행할 담당자와 승인자가 정해짐.

현재 구현된 credential key 환경 변수는 다음과 같습니다.

```text
CREDENTIAL_ACTIVE_KEY_ID      # active write key ID
CREDENTIAL_ENCRYPTION_PUBLIC_KEYS   # ID → unpadded base64 PKIX DER RSA public key
CREDENTIAL_ENCRYPTION_PRIVATE_KEYS  # ID → unpadded base64 PKCS#8/PKCS#1 DER RSA private key
CREDENTIAL_ENCRYPTION_KEYS          # optional legacy ID → 32-byte symmetric KEK
CREDENTIAL_ENCRYPTION_KEY           # optional legacy raw-ciphertext read key
```

API service account에는 active ID와 public map만 전달합니다. Private map과 legacy 값은 worker/maintenance service account에만 전달하며 새 write에는 사용되지 않습니다. Operator CLI가 있어도 production 전환 전에 staging에서 실제 dry-run/execute count, 재시작, CAS skip, corrupt-row 실패를 별도 증거로 남깁니다.

## 3. 공통 사전 점검

```sh
git rev-parse HEAD
sh -n scripts/db-migrate-url.sh
MIGRATION_DATABASE_URL="$STAGING_MIGRATION_DATABASE_URL" \
  COCKROACH_DATABASE=jandibat \
  MIGRATIONS_DIR="$PWD/db/migrations" TMPDIR=/tmp \
  scripts/db-migrate-url.sh
curl --fail --silent --show-error "$STAGING_BASE_URL/healthz"
```

`scripts/db-migrate-url.sh`는 `MIGRATION_DATABASE_URL`, `COCKROACH_DATABASE`, `MIGRATIONS_DIR`, Cockroach CLI와 쓰기 가능한 임시 디렉터리(`TMPDIR`)를 요구하며 `DATABASE_URL` fallback은 없습니다. 현재 database가 `COCKROACH_DATABASE`와 일치하는지 먼저 확인합니다. 그 뒤 pending migration을 적용하고, 이미 적용된 migration은 worktree SHA-256 checksum과 비교합니다. 이 script에는 `verify` subcommand나 dry-run mode가 없으므로 실행은 staging DB를 변경할 수 있습니다. 승인된 migration window와 전용 migration credential 없이는 실행하지 않습니다. `scripts/db-migrate.sh`는 local Docker Compose 전용입니다.

추가로 다음을 기록합니다.

- environment, UTC 시작 시각, release SHA와 image digest.
- 회전 대상 key의 ID와 생성 시각. key material은 기록하지 않음.
- 현재 key별 ciphertext row 수, `credential_decrypt_operations_total` 증가율, provider sync/auth 실패율과 `5xx` baseline.
- 최근 backup job과 격리 restore drill 링크.
- 승인자와 change/incident ticket.

명령 또는 script가 아직 존재하지 않으면 `NOT RUN`으로 기록하고 회전을 중단합니다.

## 4. Credential encryption key 무중단 회전

예시는 `cred-2026-08-a`에서 `cred-2026-11-a`로 전환합니다. 실제 key ID에는 secret이나 고객 식별자를 넣지 않습니다. 아래 절차는 구현된 runtime 동작을 기준으로 하지만 이 저장소에는 staging 실행 증거가 아직 없으므로, production에서 바로 시작하지 않습니다.

### 단계 A — 새 key 배포(decrypt 가능, write 비활성)

1. Secret manager 또는 격리된 generation host에서 RSA-3072 key pair를 만들고 public PKIX DER/private PKCS#8 DER를 `cred-2026-11-a` version으로 저장합니다. CLI stdout, shell history, PR, ticket에 private key 원문을 출력하지 않습니다.
2. Public/private map에 구/new pair를 모두 넣되 `CREDENTIAL_ACTIVE_KEY_ID=cred-2026-08-a`를 유지합니다. API에는 public map만, worker/maintenance에는 두 map을 전달합니다. Legacy row가 있으면 symmetric legacy values도 worker/maintenance에만 유지합니다.
3. staging canary 1개를 배포합니다.
4. Canary의 `/healthz` 응답과 기존 provider connection sync를 확인해 구 envelope와 legacy raw row를 계속 읽을 수 있는지 검증합니다. `/healthz`는 process health 신호일 뿐 별도 dependency readiness를 증명하지 않으므로 sync 결과를 생략하지 않습니다.
5. 구/legacy key를 사용하는 synthetic connection sync가 모두 성공하고, 15분 동안 provider sync failure 증가가 1 percentage point 미만, `5xx` 증가가 0.1 percentage point 미만이며 `credential.reencrypt` event의 `failed=0`인지 확인합니다.

실패하면 canary를 이전 image/secret version으로 되돌립니다. Maintenance process는 시작 직후 실행되므로 이 단계에서도 legacy raw row를 현재 active v3 envelope로 정규화할 수 있습니다. 따라서 DB가 전혀 바뀌지 않는다고 가정하지 않고 `credential.reencrypt` audit count를 기록합니다.

### 단계 B — 새 key를 active write로 전환

1. `CREDENTIAL_ACTIVE_KEY_ID=cred-2026-11-a`로 canary를 배포합니다.
2. synthetic provider connection을 생성하고 새 ciphertext가 authenticated `JDBK` v3 envelope이며 active RSA key ID와 wrapped per-record DEK를 포함하는지 검증합니다. Ciphertext, DEK, private key 또는 token 원문은 출력하지 않습니다.
3. token 원문이 API/log/audit에 나타나지 않는지 검사합니다.
4. 15분 안정화 후 전체 instance를 전환합니다.

다음 중 하나면 즉시 promotion을 중단합니다.

- 구/신 key를 사용하는 synthetic connection 중 하나라도 sync 실패.
- `credential.reencrypt` event의 `failed`가 1 이상.
- auth/provider connection 실패율이 baseline보다 1 percentage point 이상 증가.
- `5xx`가 5분 동안 2% 초과.
- 새 write envelope가 구 key ID를 포함하거나 active key로 복호화되지 않는 경우.

Rollback은 active write ID를 구 key로 복원하되, 새 public/private pair는 read 가능 상태로 유지해야 합니다. 이미 생성된 새 ciphertext 때문에 새 private key를 제거하면 안 됩니다.

### 단계 C — 기존 row 재암호화

독립 maintenance process가 시작 직후 실행되고 이후 `REENCRYPTION_INTERVAL`마다 반복됩니다. 수동 실행은 `scripts/rotate-credentials.sh --dry-run --scope <ticket>`로 inventory를 확인한 뒤 승인 후 `--execute --resume`을 사용합니다. Wrapper는 `MAINTENANCE_BIN`(기본 `/jandibat-maintenance`)의 `reencrypt` command를 호출합니다. 기본 주기는 1시간, run timeout은 15분, page 크기는 500입니다. 다음 read-only SQL로 수렴 상태를 확인합니다.

```sql
SELECT secret_kind, key_id, count(*) AS row_count
FROM (
  SELECT 'access' AS secret_kind,
         COALESCE(access_token_key_id, '<legacy-or-unindexed>') AS key_id
  FROM provider_connections
  WHERE access_token_ciphertext IS NOT NULL
  UNION ALL
  SELECT 'refresh' AS secret_kind,
         COALESCE(refresh_token_key_id, '<legacy-or-unindexed>') AS key_id
  FROM provider_connections
  WHERE refresh_token_ciphertext IS NOT NULL
  UNION ALL
  SELECT 'provider_revoke' AS secret_kind,
         COALESCE(token_key_id, '<legacy-or-unindexed>') AS key_id
  FROM provider_token_revocation_jobs
  WHERE token_ciphertext IS NOT NULL
) AS credential_keys
GROUP BY secret_kind, key_id
ORDER BY secret_kind, key_id;

SELECT occurred_at, outcome, metadata
FROM audit_events
WHERE action = 'credential.reencrypt'
ORDER BY occurred_at DESC
LIMIT 20;
```

- 별도 format column이 없으므로 maintenance는 암호화된 row를 `(secret kind, row ID)` 순서의 bounded page로 모두 scan합니다. 현재 active RSA key의 정상 v3 envelope만 cheap skip하며, v1/v2/raw row는 v3로 재암호화합니다.
- Update는 기존 key ID와 ciphertext를 함께 비교하는 CAS이며 concurrent credential refresh가 이기면 `skipped`를 증가시킵니다.
- Corrupt row 하나는 `failed`를 증가시키되 뒤 row 처리를 계속합니다. Run 결과는 `scanned`, `rotated`, `skipped`, `failed`, `active_key_id`만 audit metadata에 남깁니다.
- 주기 background run이 중단되면 다음 run은 처음부터 bounded scan하되 이미 current v3인 row를 cheap skip해 forward progress를 재개합니다. Operator는 `--resume`으로 `maintenance_checkpoints` cursor를 재개합니다.
- `failed > 0`, audit event `outcome=failed`, timeout 또는 같은 non-active count가 두 run 연속 감소하지 않으면 promotion을 중단합니다.

Row rollback은 구 key로 다시 대량 암호화하지 않고, 양쪽 key를 유지한 채 원인을 수정한 후 forward resume합니다. 신/구 key가 map에 모두 있으므로 active ID rollback 뒤에도 이미 생성된 신 key envelope를 읽을 수 있습니다.

### 단계 D — 구 key 폐기

다음 조건을 24시간 연속 충족해야 합니다.

- Provider connection access/refresh와 provider revoke queue를 합쳐 구 key ID ciphertext row 0건.
- 최신 `credential.reencrypt` event가 `succeeded`이고 `failed=0`.
- backup/restore drill에서 새 key secret version으로 synthetic credential 검증 성공.
- 모든 instance가 새 image와 active key ID를 보고.
- 회전 audit event와 staging evidence가 완결.

`credential_decrypt_operations_total{key_id,outcome}`의 구 key 증가가 0인지 확인합니다. 저장된 미등록 envelope ID는 `unknown`으로 축약되며 실패 outcome은 별도 조사 대상입니다. 구 private key를 worker/maintenance keyring에서 제거한 canary를 30분 운영하고 모든 provider connection synthetic sync가 성공한 뒤 전체 배포합니다. API의 구 public key도 같은 promotion에서 제거합니다. 이 검증을 자동화하지 못했으면 구 key 제거를 `NOT RUN`으로 남깁니다. Secret manager private-key version 파괴는 추가 7일 quarantine, Security Owner 2인 승인, backup 보존 기간 전체의 decrypt-only archive를 요구합니다. 파괴 뒤 rollback은 불가능합니다.

## 5. Session secret 처리

현재 session은 서명 token이 아닙니다. API는 32-byte 난수의 opaque token을 발급하고 SHA-256 digest를 DB에서 찾아 인증하며 기본 TTL은 30일입니다. `SESSION_SIGNING_KEY`는 이 경로에 주입되지 않으므로 값을 바꾸거나 삭제해도 session 검증 key rotation이 되지 않고 기존 session도 revoke되지 않습니다.

유출된 session에 대응할 때는 session revoke 기능으로 해당 session을 폐기하고, 전역 폐기가 필요하면 별도 승인된 운영 절차를 마련합니다. `SESSION_SIGNING_KEY` 자체가 유출되었더라도 현재 session token을 위조하는 데 쓰이지는 않지만, production 필수 secret이므로 새 독립 난수로 교체하고 시작 검사를 통과하는지만 확인합니다. 이를 "session rotation 완료" 증거로 기록하지 않습니다.

향후 signing 또는 keyring이 session 경로에 실제 연결되면 다음 목표 절차를 활성화합니다.

1. 새 key를 verify 가능 목록에 추가하고 구 key를 active signer로 유지.
2. canary에서 새 key ID로 synthetic session/token을 발급·검증.
3. 새 key를 active signer로 전환.
4. 구 key로 서명된 token은 최대 session TTL 또는 24시간 중 더 긴 기간 동안 verify-only로 유지.
5. 구 key verify 사용량이 0이고 모든 기존 session이 만료/revoke된 뒤 제거.

서명 검증 실패가 baseline보다 1 percentage point 증가하면 active signer를 구 key로 되돌리고 새 key를 verify 목록에 유지합니다. 유출 대응에서는 overlap 없이 구 key를 폐기하고 모든 session을 revoke합니다. 이 경우 사용자 재로그인이 필요하며 incident 공지를 남깁니다.

그 전에는 이 단계들을 실행 가능한 현재 절차로 표시하지 않습니다.

## 6. Custom provider ingestion key 회전

이 key는 사용자 요청 기반이며 overlap을 제공하지 않습니다.

`CUSTOM_PROVIDER_RELAY_ID`는 소유자에게 보이는 GraphQL `CustomProvider.id`입니다. HTTP ingest 경로에 사용하는 raw `ingestProviderID`와 혼동하지 않습니다. `ROTATION_COOKIE_JAR`는 저장소 밖 비밀 관리 도구가 만든 `0600` 권한의 격리 세션 cookie 파일이며, `ROTATION_ALLOWED_ORIGIN`은 서버 allowlist의 정확한 Web origin입니다. `ROTATION_SECRET_DIR`는 저장소 밖의 접근 제한된 임시/secret-manager staging 디렉터리여야 합니다. 세션 값 자체를 명령 인자·채팅·로그에 넣지 않습니다.

```sh
umask 077
: "${ROTATION_SECRET_DIR:?Set an external secret-manager staging directory}"
repo_root=$(CDPATH= cd -- "$(git rev-parse --show-toplevel)" && pwd -P)
rotation_dir=$(CDPATH= cd -- "$ROTATION_SECRET_DIR" && pwd -P)
case "$rotation_dir/" in
  "$repo_root/"*) echo 'Rotation response directory must be outside the repository' >&2; exit 1 ;;
esac
rotation_response=$(mktemp "$rotation_dir/custom-provider-key-rotation.XXXXXX")
chmod 600 "$rotation_response"
jq -n --arg id "$CUSTOM_PROVIDER_RELAY_ID" \
  '{query:"mutation RotateCustomProviderKey($input:RotateCustomProviderKeyInput!){rotateCustomProviderKey(input:$input){errors{code field message} ingestionKey createdAt}}",operationName:"RotateCustomProviderKey",variables:{input:{id:$id}}}' \
  | curl --fail --silent --show-error --request POST \
      --url "$API_BASE_URL/graphql" \
      --header 'Content-Type: application/json' \
      --header "Origin: $ROTATION_ALLOWED_ORIGIN" \
      --cookie "$ROTATION_COOKIE_JAR" \
      --data-binary @- --output "$rotation_response"
jq -e '(.errors == null) and (.data.rotateCustomProviderKey.errors == []) and (.data.rotateCustomProviderKey.ingestionKey | type == "string" and length > 0)' \
  "$rotation_response" >/dev/null
```

`$rotation_response` 파일은 owner에게 새 key를 전달하는 임시 보안 경계입니다. 명령을 저장소 밖에서 실행하더라도 경로 검사와 `0600` mode를 유지합니다. Secret manager 반입 뒤 조직의 secure deletion 정책에 따라 처리하며 Git diff, CI artifact, terminal stdout에 포함하지 않습니다.

1. 소유자 재인증과 subject ownership을 확인.
2. GraphQL `rotateCustomProviderKey` mutation을 한 번 호출하고 응답 key를 안전한 password/secret manager로 즉시 전달. HTTP 200이어도 `errors` 또는 typed `errors`가 있으면 실패입니다. 현재 client-address 기준 제한은 시간당 5회입니다.
3. 응답 body를 log, trace, CI artifact에 저장하지 않음.
4. 구 key ingest가 `401`, 새 key ingest가 성공하는지 synthetic event ID로 확인.
5. 응답의 `X-Request-ID`와 실행 시각을 기록하되 key 원문은 기록하지 않음.
6. Production `audit_events`에서 같은 request ID의 `http.mutation.intent`(generic `graphql` target)와 성공 outcome `post.graphql`을 조정합니다. 성공 outcome은 actor type `user`, target type `custom_provider`, target ID는 opaque Relay ID가 아닌 canonical raw provider UUID여야 합니다. 안전한 operation name `RotateCustomProviderKey`의 관측 이벤트와 연결하며 key 원문은 기록하지 않습니다.

HTTP mutation은 handler 전에 Cockroach sink에 write-ahead audit intent를 append하며 실패하면 503으로 fail closed합니다. 성공 outcome은 state와 같은 transaction의 mutation audit outbox에 enqueue되고 commit된 뒤에만 buffered HTTP response가 공개됩니다. Outcome delivery는 worker가 append-only sink로 수행하며 verifier가 intent/outcome과 delivered outbox를 조정합니다. 둘 다 없거나 stale/dead outbox가 있으면 운영 성공으로 닫지 말고 incident로 조정합니다. 구 key가 계속 성공하면 보안 실패입니다. 새 key 전달에 실패했더라도 구 key를 되살리지 않고 다시 rotate합니다.

## 7. Provider OAuth token rotation/revoke

- Disconnect는 먼저 local access/refresh ciphertext를 지우고 connection-owned fact/sync data를 purge한 뒤 credential-free `revoked` tombstone만 최대 30일 유지합니다. Get/list에서는 tombstone을 숨깁니다.
- OAuth2 access token은 local 삭제 transaction에서 encrypted `provider_token_revocation_jobs`로 이동합니다. Credential-bearing worker가 GitHub application-token revoke 또는 GitLab OAuth revoke endpoint에 전달하며, redirect를 따르지 않고 고정 endpoint, bounded timeout/body, secret-redacted 오류를 사용합니다. Codeberg는 호환되는 공식 revoke 계약을 보장할 수 없어 local-only입니다.
- Provider revoke 실패는 이미 완료된 local credential 삭제를 rollback하거나 secret을 오류에 노출하지 않습니다. Durable job은 claim-token lease와 backoff로 재시도하고 성공 시 삭제됩니다. 최대 시도 횟수를 소진하면 worker는 lease/claim을 해제하고 `dead`, `terminal_at`, 안정된 `terminal_reason=max_attempts_exhausted`로 원자 전이합니다. Provider 응답이나 token은 terminal reason에 저장하지 않습니다. `provider_token_revocation_jobs_dead_terminal_idx`로 신규 dead job을 경보하고, 원인 수정 후에는 별도 승인된 replay/follow-up 구현을 통해서만 재처리합니다. 행의 status/check를 수동 변경하지 않습니다. Refresh-token 자동 교체는 아직 구현되지 않았습니다.
- Connection mutation의 audit 정책은 audit runbook을 따릅니다. 해당 request ID의 필수 audit evidence가 없으면 운영 성공으로 닫지 않습니다.

## 8. Backup external connection credential 회전

CockroachDB v26.2 external connection은 연결 URI 갱신으로 credential을 교체할 수 있습니다.

```sql
ALTER EXTERNAL CONNECTION jandibat_backup_v1
AS '<secret-manager가 안전하게 렌더링한 새 storage URI>';

CHECK EXTERNAL CONNECTION 'external://jandibat_backup_v1';
```

URI 원문을 shell history나 staging evidence에 복사하지 않습니다. 변경 직후 full backup과 격리 restore를 수행합니다. 실패 시 old storage credential이 아직 유효한 overlap 안에서 external connection URI를 이전 secret version으로 되돌립니다.

## 9. 증거와 종료 조건

- 각 단계 시작/종료 UTC 시각과 승인자.
- key ID별 row count, `credential_decrypt_operations_total` 증가율과 provider sync/auth/5xx dashboard 링크.
- `credential.reencrypt` audit summary와 key-ID count. 원문 secret/ciphertext 제외.
- Operator CLI/wrapper의 실행 mode, scope, resume 여부와 exit code. 구현 여부와 실제 staging 호출 증거를 구분해 기록.
- canary와 rollback 여부, 최종 image digest.
- custom/provider/external connection 검증 결과.
- audit event ID와 backup/restore evidence 링크.

모든 항목은 `docs/evidence/staging/README.md` 템플릿에 기록합니다. 한 단계라도 `NOT RUN`이면 Phase 3 rotation은 미완료입니다.
