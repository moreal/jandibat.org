# 런타임 설정

API, sync worker, maintenance 프로세스는 환경 변수만 읽습니다. 로컬에서는 `.env.example`을 `.env`로 복사할 수 있지만 애플리케이션이 `.env`를 자동 로드하지는 않습니다. 현재 shell에 내보낸 뒤 실행합니다.

```sh
cp .env.example .env
set -a
. ./.env
set +a
make dev-api
```

`.env`와 `.env.*`는 ignore되고 `.env.example`만 추적합니다. 실제 secret, access token, 개인 이메일은 예제 파일이나 저장소에 넣지 않습니다.

## 개발과 CI 도구 버전

Go, Node.js, Yarn과 `lint-api`가 사용하는 Go 정적 분석기의 버전은 루트 `flake.nix`와 `flake.lock`에서만 관리합니다. 로컬과 CI의 전체 검증 명령은 다음과 같습니다.

```sh
nix develop --command make ci
```

GitHub Actions는 commit SHA로 고정한 Nix 설치 및 캐시 action을 사용하고, 언어 도구를 쓰는 Make 대상을 `nix develop --command`로 실행합니다. Workflow에 `setup-go`, `setup-node`, Corepack 활성화 명령, 또는 별도의 `GO_VERSION`, `NODE_VERSION`, `YARN_VERSION` 환경 변수를 추가하지 않습니다. `make ci-version-authority-check`가 모든 GitHub workflow YAML에서 이 규칙을 검증합니다.

## 변수 목록

| 변수 | 개발 기본값 | 형식과 용도 |
| --- | --- | --- |
| `APP_ENV` | `development` | `development` 또는 `production` |
| `BUILD_SHA` | `unknown` | 모든 metric/log를 immutable image source에 연결하는 full commit SHA |
| `REGION` | `unknown` | metric/log resource label에 사용할 배포 region/cluster locality |
| `API_ADDR` | `:8080` | Go HTTP server listen address |
| `WORKER_HEALTH_ADDR` | `:8081` | Sync worker의 `/livez`, `/readyz` listen address |
| `MAINTENANCE_HEALTH_ADDR` | `:8082` | Maintenance worker의 `/livez`, `/readyz` listen address |
| `PUBLIC_BASE_URL` | `http://localhost:8080` | callback과 공개 링크에 사용할 절대 URL |
| `WEB_BASE_URL` | `http://localhost:5173` | 허용할 웹 앱의 절대 URL |
| `DATABASE_URL` | 빈 값 | API 전용 DB 접속 문자열. Production username은 `jandibat_api`로 고정. 로컬 Compose는 `postgresql://root@localhost:26257/jandibat?sslmode=disable` |
| `WORKER_DATABASE_URL` | 빈 값 | queued/scheduled sync 전용 DB 접속 문자열. Production username은 `jandibat_worker`로 고정 |
| `MAINTENANCE_DATABASE_URL` | 빈 값 | retention/re-encryption 전용 DB 접속 문자열. Production username은 `jandibat_maintenance`로 고정 |
| `DELETION_PSEUDONYM_KEY` | 빈 값 | account 삭제 완료 감사 대상 pseudonym용 unpadded base64url, decode 후 32바이트 이상. Maintenance에만 주입하며 `SESSION_SIGNING_KEY`와 같으면 안 됨 |
| `DELETED_IDENTITY_HMAC_ACTIVE_KEY_ID` | 빈 값 | 삭제 identity HMAC의 active version ID. API와 maintenance에만 주입 |
| `DELETED_IDENTITY_HMAC_KEYS` | `{}` | key ID → 정확히 32바이트의 unpadded standard base64 HMAC key JSON map. API는 전체 retention window read, maintenance는 active write/read. session/deletion pseudonym/credential key와 재사용 금지 |
| `DEVELOPMENT_ALL_IN_ONE` | `false` | 개발에서만 API process에 background job을 함께 배선하는 compatibility flag. Production은 `true`를 거부 |
| `SESSION_SIGNING_KEY` | 빈 값 | unpadded standard base64. production 시작 검사에서 decode 후 32바이트 이상 필요. 현재 opaque session 발급·검증에는 사용되지 않음 |
| `CREDENTIAL_ACTIVE_KEY_ID` | 빈 값 | active credential RSA key ID. production 필수이며 public map에 존재해야 하고 worker/maintenance에서는 private map에도 존재해야 함 |
| `CREDENTIAL_ENCRYPTION_PUBLIC_KEYS` | 빈 JSON map | key ID에서 unpadded standard base64 PKIX DER RSA public key로의 JSON object. production의 API/worker/maintenance 모두 필수 |
| `CREDENTIAL_ENCRYPTION_PRIVATE_KEYS` | 빈 JSON map | key ID에서 unpadded standard base64 PKCS#8 또는 PKCS#1 DER RSA private key로의 JSON object. production worker/maintenance에만 필수이며 API에는 주입 금지 |
| `CREDENTIAL_ENCRYPTION_KEYS` | 빈 JSON map | v1/v2 migration용 key ID→unpadded standard base64 32-byte symmetric KEK map. worker/maintenance에만 선택적으로 주입하고 새 write에는 사용하지 않음 |
| `CREDENTIAL_ENCRYPTION_KEY` | 빈 값 | envelope 도입 전 raw AES-GCM ciphertext용 32-byte legacy key. worker/maintenance decrypt-only이며 API에는 주입 금지 |
| `SMTP_ADDR` | 빈 값 | worker 전용 SMTP 서버 `host:port`; production API에는 주입 금지 |
| `SMTP_USERNAME` | 빈 값 | worker 전용 SMTP 사용자; production API에는 주입 금지 |
| `SMTP_PASSWORD` | 빈 값 | worker 전용 SMTP 비밀번호/credential; production API에는 주입 금지 |
| `SMTP_FROM` | 빈 값 | worker 전용 Magic Link 발신 주소; production API에는 주입 금지 |
| `GITHUB_CLIENT_ID` | 빈 값 | GitHub OAuth app client ID |
| `GITHUB_CLIENT_SECRET` | 빈 값 | GitHub OAuth app secret |
| `GITLAB_CLIENT_ID` | 빈 값 | GitLab OAuth app client ID |
| `GITLAB_CLIENT_SECRET` | 빈 값 | GitLab OAuth app secret |
| `CODEBERG_CLIENT_ID` | 빈 값 | Codeberg OAuth app client ID |
| `CODEBERG_CLIENT_SECRET` | 빈 값 | Codeberg OAuth app secret |
| `SCHEDULER_INTERVAL` | `15m` | 양수인 Go duration |
| `SHUTDOWN_TIMEOUT` | `40s` | SIGINT/SIGTERM 뒤 graceful shutdown 제한, 양수인 Go duration. HTTP write timeout(35초)보다 길어야 합니다. |
| `RETENTION_INTERVAL` | `24h` | retention purge 실행 주기, 양수인 Go duration |
| `REENCRYPTION_INTERVAL` | `1h` | credential re-encryption 실행 주기, 양수인 Go duration |
| `MAINTENANCE_TIMEOUT` | `15m` | retention/re-encryption 한 번의 실행 제한, 양수인 Go duration |
| `MAINTENANCE_BATCH_SIZE` | `500` | maintenance DB batch 크기, 양의 정수 |
| `RETENTION_MAX_BATCHES` | `30` | dataset별 한 retention run의 최대 batch 수, 양의 정수 |
| `TRUST_PROXY_HEADERS` | `false` | boolean. 신뢰 proxy 배포에서만 활성화 |

빈 문자열이나 공백뿐인 값은 기본값과 동일하게 취급합니다. URL은 scheme과 host가 모두 있는 절대 URL이어야 하고 duration은 `30s`, `15m`, `1h` 같은 Go 형식을 사용합니다.

## Production secret 생성

Session secret과 credential RSA private key는 서로 독립적으로 생성합니다. 명령 결과를 shell history, CI 로그, PR 또는 티켓에 붙이지 말고 배포 환경의 secret manager에 바로 저장합니다. RSA는 최소 2048-bit이며 신규 운영 key는 3072-bit를 권장합니다.

```sh
# SESSION_SIGNING_KEY: 48 random bytes (32 bytes 이상)
openssl rand -base64 48 | tr -d '\n='

# RSA private PEM은 권한이 제한된 임시 경로에 만들고 작업 뒤 폐기합니다.
umask 077
openssl genpkey -algorithm RSA -pkeyopt rsa_keygen_bits:3072 \
  -out credential-private.pem
openssl pkey -in credential-private.pem -pubout -outform DER \
  | openssl base64 -A | tr -d '=' > credential-public.der.b64
openssl pkcs8 -topk8 -nocrypt -in credential-private.pem -outform DER \
  | openssl base64 -A | tr -d '=' > credential-private.der.b64
```

생성한 DER base64 값은 `.env`나 저장소에 남기지 않고 secret manager에서 다음 세 설정으로 조립합니다. Public map은 세 process에, private map은 worker/maintenance service account에만 공개합니다. 아래 값은 형식 예시이며 실제 key material이 아닙니다.

```sh
CREDENTIAL_ACTIVE_KEY_ID=cred-2026-08-a
CREDENTIAL_ENCRYPTION_PUBLIC_KEYS='{"cred-2026-08-a":"<unpadded-base64-pkix-der>"}'
CREDENTIAL_ENCRYPTION_PRIVATE_KEYS='{"cred-2026-08-a":"<unpadded-base64-pkcs8-der>"}'
```

두 asymmetric map은 JSON object여야 하며 같은 ID의 RSA public/private key가 일치해야 합니다. Public key는 PKIX DER, private key는 PKCS#8 또는 PKCS#1 DER이고 최소 RSA-2048이어야 합니다. `APP_ENV=production`에서 API는 active public key와 32바이트 이상의 `SESSION_SIGNING_KEY`를 요구하고 private/symmetric/legacy key가 하나라도 주입되면 시작을 거부합니다. Worker와 maintenance는 active public/private pair를 요구하며 legacy symmetric 값은 v1/v2 row가 남은 전환 기간에만 받습니다.

## 현재 key 사용과 rotation 지원 상태

설정 이름만 보고 구현된 기능을 추정하지 않습니다. 현재 API server의 실제 경계는 다음과 같습니다.

- Session은 32-byte 난수의 base64url opaque token을 발급하고 SHA-256 digest를 DB에 저장해 검증합니다. `SESSION_SIGNING_KEY`는 production 시작 조건과 credential key 분리 검사에는 쓰이지만 token 서명/검증 경로에는 연결되어 있지 않습니다. 이 환경 변수만 바꿔도 기존 session이 무효화되지는 않습니다.
- Provider connection access/refresh token의 새 write는 매번 random 256-bit DEK로 AES-GCM 암호화하고 active RSA public key로 DEK를 OAEP-SHA-256 wrap한 authenticated `JDBK` v3 envelope로 저장합니다. Internet-facing API는 public key만 보유하므로 저장 ciphertext를 복호화할 수 없습니다.
- Sync worker와 maintenance만 RSA private key를 보유해 v3를 읽습니다. `CREDENTIAL_ENCRYPTION_KEYS`와 `CREDENTIAL_ENCRYPTION_KEY`는 기존 v1/v2/raw ciphertext를 maintenance가 v3로 이행하기 위한 transition-only read material이며 새 write에는 선택되지 않습니다.
- CockroachDB maintenance process는 시작 직후와 `REENCRYPTION_INTERVAL`마다 connection access/refresh token 및 pending provider-revoke queue token의 re-encryption worker를 실행합니다. Worker는 `MAINTENANCE_BATCH_SIZE` cursor page와 ciphertext/key-ID CAS로 concurrent refresh/claim을 덮어쓰지 않고 active v3 envelope로 변환하며 `credential.reencrypt` audit event에 count를 기록합니다.
- Operations layer의 credential re-encryption은 durable checkpoint/resume를 제공하고 `/bin/maintenance reencrypt` 및 `scripts/rotate-credentials.sh`에서 dry-run/execute로 호출할 수 있습니다. 주기 background run도 encrypted row를 bounded cursor로 scan해 current active-key v3만 skip하고 v1/v2/raw row를 v3로 변환하며, 오류는 maintenance log/audit에 남겨 다음 interval에 재시도합니다.
- Custom provider `ingestionKey`는 credential keyring으로 암호화하지 않습니다. 서버는 SHA-256 digest만 저장하고 create/rotate 응답에서 평문을 한 번 반환합니다. 따라서 credential ciphertext re-encryption 대상으로 취급하지 않습니다.

Production 회전은 구/new RSA pair를 두 map에 함께 둔 뒤 active ID만 전환하고, DB row와 audit count가 수렴한 뒤 구 private key를 제거하는 순서를 지켜야 합니다. API에는 회전 내내 public map만 전달합니다. 절차와 중단 기준은 `docs/runbooks/KEY_ROTATION.ko.md`를 따릅니다.

Development에서 asymmetric/legacy map과 legacy key가 모두 비어 있으면 서버는 `development-only` symmetric fallback을 사용합니다. 이는 로컬 편의 기능이며 공유·staging·production 데이터 암호화에 사용하지 않습니다. Asymmetric key를 명시한 all-in-one 개발 프로세스는 matching private key도 필요합니다.

## Production 시작 조건

Production은 API, worker, maintenance의 별도 image를 서로 다른 container와 DB role로 실행합니다. 각 process에는 자기 DSN만 주입하고 다른 process의 DSN을 전달하지 않습니다. Image별 port, probe, mount, migration Job 계약은 [`IMAGE_RUNTIME_CONTRACT.ko.md`](IMAGE_RUNTIME_CONTRACT.ko.md)를 따릅니다.

| Process | 실행 파일 | DB 환경 변수와 고정 username | 비밀 범위 |
| --- | --- | --- | --- |
| API | `/bin/server` | `DATABASE_URL`, `jandibat_api` | session, OAuth app secret, credential RSA public key, identity HMAC keyring; SMTP credential 없음 |
| Sync worker | `/bin/worker` | `WORKER_DATABASE_URL`, `jandibat_worker` | provider token RSA keypair/legacy read key, SMTP credential, GitHub/GitLab remote revoke용 OAuth app secret |
| Maintenance | `/bin/maintenance` | `MAINTENANCE_DATABASE_URL`, `jandibat_maintenance` | re-encryption RSA keypair/legacy read key, account 삭제용 `DELETION_PSEUDONYM_KEY`, identity HMAC keyring |

Operator wrapper의 `MAINTENANCE_BIN` 기본값은 `/bin/maintenance`입니다. 별도 restore-tools image는 복구 검증 계약의 `/jandibat-api`, `/jandibat-maintenance` 경로를 유지하며 `RESTORE_API_BIN`, `RESTORE_MAINTENANCE_BIN`으로 지정합니다.

고정 username이 다르므로 같은 권한 role을 다른 비밀번호나 query parameter의 DSN으로 가장할 수 없습니다. Production에서 DSN이 없거나 username이 다르면 listener를 열기 전에 시작을 거부합니다. `DEVELOPMENT_ALL_IN_ONE=true`도 production에서 거부합니다.

`config.LoadForProcess`의 형식 검사에 더해 API composition은 `APP_ENV=production`에서 다음 조건을 모두 확인합니다.

- `DATABASE_URL`이 비어 있지 않음.
- `PUBLIC_BASE_URL`과 `WEB_BASE_URL`의 scheme이 모두 `https`임.
- `SESSION_SIGNING_KEY`가 32바이트 이상이고 `CREDENTIAL_ACTIVE_KEY_ID`가 유효한 RSA public key를 선택함.
- API에 `CREDENTIAL_ENCRYPTION_PRIVATE_KEYS`, `CREDENTIAL_ENCRYPTION_KEYS`, `CREDENTIAL_ENCRYPTION_KEY`가 주입되지 않음.
- API에 `SMTP_ADDR`, `SMTP_USERNAME`, `SMTP_PASSWORD`, `SMTP_FROM`이 주입되지 않음. Durable mail outbox를 claim하는 worker에는 네 값이 모두 설정됨.
- GitHub, GitLab, Codeberg 각각의 client ID와 client secret이 모두 설정됨.

하나라도 빠지면 해당 production process는 listener를 열기 전에 시작을 거부해야 합니다. Development의 명시적 all-in-one process에서는 SMTP 네 값을 함께 설정하거나 모두 생략해 memory mailer를 사용할 수 있고, `DATABASE_URL`을 생략하면 memory store를 사용합니다. 부분적으로만 채운 SMTP/OAuth 설정은 유효한 fallback으로 간주하지 않습니다.

## 배포 주의사항

- `PUBLIC_BASE_URL`과 `WEB_BASE_URL`은 운영 HTTPS origin으로 명시하고 요청의 `Host` header에서 동적으로 만들지 않습니다.
- OAuth provider에 등록한 callback URL은 `PUBLIC_BASE_URL` 기반의 정확한 URL과 일치시킵니다.
- `TRUST_PROXY_HEADERS=true`는 애플리케이션에 직접 접근할 수 없고 proxy가 외부의 forwarding header를 제거한 뒤 신뢰 가능한 값으로 다시 쓰는 경우에만 설정합니다.
- SMTP/OAuth credential, session key, credential key map, deletion pseudonym key, identity tombstone HMAC keyring은 각각 독립된 secret으로 접근 권한을 최소화하고 값을 재사용하지 않습니다. Runtime은 decode 가능한 session/deletion/HMAC/credential symmetric·RSA material이 서로 다른 domain에서 byte-identical하면 production 시작을 거부합니다. SMTP/OAuth처럼 형식이 다른 credential은 배포 전 secret inventory/digest 검토로 분리를 확인합니다.
- API, worker, maintenance는 별도 container/service account로 실행하고 각각 자기 DB DSN만 주입합니다. Worker와 maintenance에 `DATABASE_URL`, API에 worker/maintenance DSN을 전달하지 않습니다. Worker는 durable GitHub/GitLab token revoke를 위해 `PUBLIC_BASE_URL`과 두 provider의 OAuth app credential도 받지만 Codeberg secret은 받지 않습니다.
- API는 HTTP/auth/audit intent/enqueue만, worker는 scheduled/queued sync만, maintenance는 retention/re-encryption만 실행합니다. 개발에서 단일 process가 필요할 때만 `DEVELOPMENT_ALL_IN_ONE=true`를 명시합니다.
- CockroachDB table별 GRANT와 검증 절차는 `docs/runbooks/DATABASE_ROLES.ko.md`를 따릅니다.
- 종료 제한인 `SHUTDOWN_TIMEOUT`은 HTTP write timeout(35초)보다 길고, load balancer drain 시간과 배포 플랫폼의 강제 종료 제한보다 짧게 둡니다.
