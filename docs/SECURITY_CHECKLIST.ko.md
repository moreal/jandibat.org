# 인증·연동 보안 검토 기준

기준일: 2026-08-13

이 문서는 단순 체크박스가 아니라 **control ID → 적용 여부 → 실행 증거 → 검토자**를 연결하는 릴리스 gate입니다. 보안 영향이 있는 PR은 [`docs/evidence/security/README.md`](evidence/security/README.md)의 템플릿으로 검토 기록을 만들고 `make security-review-check SECURITY_REVIEW=<기록 경로>`를 통과해야 합니다. 구현 파일이 있거나 테스트 이름이 그럴듯하다는 사실만으로 `PASS`를 부여하지 않습니다.

Validator는 full 40-hex reviewed SHA, `@github-login` reviewer, UTC RFC3339, immutable GitHub Actions run/artifact URL+64-hex digest 또는 command+artifact SHA-256 문법을 강제합니다. PR/release gate는 reviewed SHA 이후 review record 외 code drift를 허용하지 않으며 실제 사람이 검토하지 않은 자동 PASS record를 만들지 않습니다.

허용 상태는 다음뿐입니다.

- `PASS`: 해당 commit/image에서 실행한 증거가 control을 직접 검증한다.
- `N/A`: 변경 범위에 적용되지 않으며 구체적인 이유와 검토자가 있다.
- `FAIL`: 검증했으나 control을 충족하지 않는다. 릴리스 차단 상태다.
- `NOT RUN`: 실행하지 않았거나 증거를 확인하지 못했다. 릴리스 차단 상태다.

비밀값, 실제 이메일·subject, cookie, authorization code, assertion, ciphertext, external storage URI는 검토 기록과 artifact에 넣지 않습니다. CI URL은 commit SHA와 실행 결과를 확인할 수 있어야 하고, 로컬 명령은 stdout 주장 대신 원시 결과 artifact와 SHA-256을 연결합니다.

## Control 목록

### 공통 HTTP·인증 경계

| ID | 검토할 불변 조건 | 최소 증거 |
| --- | --- | --- |
| COM-01 | 모든 외부 입력에 길이·형식·문자·개수 제한이 있다. | 계약 lint + boundary/oversized 자동 테스트 |
| COM-02 | 인증 실패가 계정·이메일 존재 여부를 문구나 관찰 가능한 분기로 노출하지 않는다. Magic Link HTTP 요청은 SMTP를 기다리지 않고 durable intent만 enqueue한다. | enumeration regression + FK-free outbox crash/role integration + staging timing 표본 |
| COM-03 | 사용자·연결·subject 조회/변경마다 서버 측 소유권을 검사한다. | 다른 사용자 접근 거부 자동 테스트 |
| COM-04 | 세션 cookie는 `Secure`, `HttpOnly`, 적절한 `SameSite`를 사용하고 로그인·권한 변경 시 회전한다. | cookie 속성 및 session rotation 테스트 |
| COM-05 | 상태 변경 요청은 CSRF 방어를 적용하고 CORS origin/method/header를 최소 허용한다. | CSRF/CORS regression test + security smoke 결과 |
| COM-06 | token/assertion/code/magic-link 원문과 비밀값이 log·trace·오류 응답에 없다. | redaction test + canary log 검색 결과 |
| COM-07 | 인증·동기화·공개 고비용 endpoint에 계정/IP/리소스 단위 분산 rate limit이 있다. | real Cockroach atomicity test + endpoint contract test |
| COM-08 | 외부·내부 오류를 안정된 공개 오류로 변환하고 stack/credential/upstream body를 전달하지 않는다. | sanitized problem response test |

### Magic Link

| ID | 검토할 불변 조건 | 최소 증거 |
| --- | --- | --- |
| MAG-01 | token은 worker 메모리에서 안전한 난수로 만들고 저장 시 FK-free delivery row에 단방향 hash만 남긴다. raw/decryptable token은 DB에 없다. | issuance/outbox schema + actual worker-role crash/replay test |
| MAG-02 | 원문 token은 query/access log/Referer에 남지 않는다. | mail adapter URL test + staging redaction 확인 |
| MAG-03 | 짧은 만료, 단일 사용, 목적·계정 binding과 concurrent consume 원자성을 보장한다. | expiry/replay/concurrency test |
| MAG-04 | 등록 여부와 무관한 동일 응답, 재전송 간격과 일일 한도가 있다. | enumeration/rate-limit test |
| MAG-05 | callback은 고정 HTTPS origin/path allowlist만 허용한다. | redirect allowlist test |
| MAG-06 | 성공 시 이전 token/session 정책을 적용하고 새 session으로 회전한다. | consumption/session rotation test |

### Passkey / WebAuthn

| ID | 검토할 불변 조건 | 최소 증거 |
| --- | --- | --- |
| WEB-01 | challenge는 안전한 난수이며 session/user/ceremony에 묶여 짧게 만료되고 한 번만 사용된다. | expiry/replay/wrong-user test |
| WEB-02 | 환경별 RP ID와 origin exact allowlist를 검증한다. | verifier adapter test |
| WEB-03 | 등록 시 credential ID/public key/attestation/sign counter를 검증·저장한다. | synthetic registration ceremony test |
| WEB-04 | 인증 시 signature/challenge/origin/RP ID hash와 UV/UP 정책을 검증한다. | synthetic authentication ceremony test |
| WEB-05 | sign counter 회귀·복제 의심을 감사하고 magic-link 재인증을 요구한다. | clone regression + audit test |
| WEB-06 | credential 이름/오류를 통해 다른 계정을 열거할 수 없다. | cross-user credential test |

### OAuth와 Provider token

| ID | 검토할 불변 조건 | 최소 증거 |
| --- | --- | --- |
| OAU-01 | state는 browser session에 묶고 지원 provider에는 PKCE S256을 사용한다. | composition/HTTP flow test |
| OAU-02 | redirect URI는 exact allowlist이고 동적 외부 redirect를 허용하지 않는다. | redirect regression test |
| OAU-03 | 최소 scope만 요청하고 private 접근은 별도 동의를 받는다. | provider configuration review |
| OAU-04 | token은 random per-record DEK + RSA-OAEP-SHA-256 v3 envelope로 암호화한다. | tamper/wrong-key/rotation test |
| OAU-05 | API process에는 public key만 주입하고 private/symmetric/legacy material을 시작 시 거부한다. | production config rejection test |
| OAU-06 | token은 UI/API/log에 원문 노출하지 않고 연결 해제 시 local purge와 가능한 provider revoke를 한다. | revoke/purge/redaction test |
| OAU-07 | webhook 도입 시 signature/timestamp/replay/body-size를 검증한다. | 미도입이면 `N/A` 사유, 도입 시 fixture test |

### Provider 수집과 Custom Provider

| ID | 검토할 불변 조건 | 최소 증거 |
| --- | --- | --- |
| PRV-01 | outbound destination은 scheme/host/port allowlist이며 redirect/DNS 해석 뒤에도 SSRF 경계를 지킨다. | loopback/link-local/RFC1918/metadata/rebinding fixture |
| PRV-02 | response size, connect/read timeout, redirect, 동시 요청 수를 제한한다. | boundary/saturation test |
| PRV-03 | payload는 계약으로 검증하고 날짜/count/metadata/batch 상한을 적용한다. | OpenAPI + partial rejection test |
| PRV-04 | provider 실패를 격리하고 retry에 backoff/jitter/최대 횟수/idempotency가 있다. | scheduler/fault fixture result |
| PRV-05 | 동일 provider event와 idempotency key replay가 추가 fact를 만들지 않는다. | migrated Cockroach durable replay test |
| PRV-06 | API/sync/maintenance DB role을 분리하고 table별 허용/거부를 실제 DB에서 검증한다. | `make db-runtime-roles-test` result |

### SVG·임베드·브라우저 경계

| ID | 검토할 불변 조건 | 최소 증거 |
| --- | --- | --- |
| SVG-01 | subject/label/theme은 escape 또는 서버 정의 값으로 제한한다. | hostile subject snapshot test |
| SVG-02 | SVG에 script/event handler/external URL/raw markup이 없다. | self-contained render test + security smoke |
| SVG-03 | 정확한 content type, CSP, `X-Content-Type-Options`를 설정한다. | HTTP contract test + staging headers |
| SVG-04 | frontend는 provider/user 문자열을 `innerHTML`에 넣지 않고 link protocol을 제한한다. | source review + browser test |
| SVG-05 | 공개 cache key에 출력 parameter가 모두 포함되고 private 응답은 공개 cache되지 않는다. | cache/privacy contract test |

### 데이터·운영·공급망

| ID | 검토할 불변 조건 | 최소 증거 |
| --- | --- | --- |
| OPS-01 | 민감 데이터 분류, 보존, 삭제/연결 해제 정리 범위가 문서화되고 자동 purge가 있다. | retention 문서 + boundary/purge result |
| OPS-02 | migration은 최소 권한 계정으로 실행하고 backup/restore/forward-fix 절차가 있다. | DB role negative test + migration/restore artifact |
| OPS-03 | 감사 event에 actor/target/outcome/time/correlation ID가 있고 비밀값을 제외한다. 0012/0013 outbox 채택 adapter는 state와 redacted success/intentional failure/denial outcome을 같은 transaction에 commit한다. | adopter registry + atomic success/failure rollback·crash dispatch + role-negative/redaction/reconciliation artifact |
| OPS-04 | dependency/secret/static/container scan에 신규 high/critical이 없다. | 해당 commit의 CI 및 image scan artifact |
| OPS-05 | 운영 secret은 secret manager로 주입하고 image/repository 예시는 가짜 값만 사용한다. | deploy manifest review + secret scan |
| OPS-06 | `TRUST_PROXY_HEADERS=true`는 forwarding header를 제거·재작성하는 신뢰 proxy 뒤에서만 사용한다. | ingress configuration evidence + source-IP regression test |
| OPS-07 | replay/만료/권한 우회/oversized/slow-body 실패 경로가 자동화돼 있다. | Go security regression + staging DAST result |
| OPS-08 | backup은 RPO 1시간, 격리 restore는 RTO 4시간을 충족하며 삭제 replay까지 검증한다. | timestamp가 있는 backup/restore drill artifact |

## 저장소 자동 증거 카탈로그

아래 경로는 control을 검토할 때 출발점이며, PR 실행 결과를 대신하지 않습니다.

- HTTP/privacy/rate-limit: `apps/api/internal/http/contract_test.go`, `security_regression_test.go`, `cache_contract_test.go`, `router_audit_test.go`
- Magic Link/Passkey: `apps/api/internal/auth/service_test.go`, `apps/api/internal/http/handlers/passkey_error_test.go`, `apps/api/internal/adapters/auth/cockroach/cockroach_integration_test.go`
- OAuth/token envelope: `apps/api/cmd/server/composition_test.go`, `apps/api/internal/integrations/connections_test.go`, `apps/api/internal/operations/asymmetric_envelope_test.go`
- Operations/Custom ingest/Cockroach replay: `apps/api/internal/adapters/operations/cockroach`, `apps/api/internal/integrations/custom_test.go`, `apps/api/internal/adapters/integrations/cockroach/cockroach_integration_test.go`
- SVG: `apps/api/internal/render/heatmap_test.go`, `apps/api/internal/http/contract_test.go`
- DB 역할: `scripts/db-configure-runtime-roles.sh`, `scripts/db-verify-runtime-roles.sh`
- 공급망/secret: `.github/workflows/ci.yml`, `scripts/check-secrets.sh`
- staging HTTP 관찰: `scripts/security-smoke.mjs`

## 릴리스 명령

```sh
make ci
make db-up db-migrate
JANDIBAT_TEST_DATABASE_URL='postgresql://root@127.0.0.1:26257/jandibat?sslmode=disable' \
JANDIBAT_TEST_API_DATABASE_URL='postgresql://jandibat_api@127.0.0.1:26257/jandibat?sslmode=disable' \
JANDIBAT_TEST_WORKER_DATABASE_URL='postgresql://jandibat_worker@127.0.0.1:26257/jandibat?sslmode=disable' \
JANDIBAT_TEST_MAINTENANCE_DATABASE_URL='postgresql://jandibat_maintenance@127.0.0.1:26257/jandibat?sslmode=disable' \
  make test-api-integration
make db-runtime-roles-test
make security-review-check SECURITY_REVIEW=docs/evidence/security/<review>.md
```

전체 이력 Gitleaks, `govulncheck`, `gosec`, Yarn audit 결과는 CI artifact/run과 연결합니다. 정규식 보조 검사는 전용 secret scanner를 대체하지 않습니다. 보안 영향이 있는 계약 변경은 `openapi/jandibat.yaml`, 생성 타입, `docs/interface-change-log.md`를 함께 갱신합니다.
