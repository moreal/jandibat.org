# 전체 로드맵 완료 체크리스트

기준일: 2026-08-13

이 문서는 `PROJECT_PLAN.ko.md`의 Phase 0~3을 실제로 완료했는지 판단하는 실행 가능한 체크리스트입니다. 체크박스는 구현 파일이 있다는 이유만으로 갱신하지 않습니다. 각 항목의 자동 검사와 수동 인수 조건을 모두 충족한 뒤 체크하고, 계약 변경은 `docs/interface-change-log.md`에 기록합니다.

## 공통 품질 게이트

- [x] OpenAPI lint 명령이 있다: `make openapi-lint`
- [x] 생성 TypeScript 타입 drift 검사가 있다: `make openapi-check`
- [x] 백엔드 테스트 명령이 있다: `make test-api`
- [x] 프론트엔드 타입검사와 빌드 명령이 있다: `make typecheck build-web`
- [x] 위 검사를 수행하는 GitHub Actions가 최소 권한과 고정 버전으로 구성돼 있다: `.github/workflows/ci.yml`
- [ ] 깨끗한 checkout에서 전체 게이트가 통과한다: `make ci`

현재 작업트리에서는 2026-08-13에 전체 gate를 실행했습니다. 다만 이 저장소의 최초 변경이 아직 커밋되지 않아 `git status --short`가 깨끗한 checkout 증거가 될 수 없으므로 위 항목은 체크하지 않습니다.

Phase 완료를 주장하기 전 공통으로 다음을 실행합니다.

```sh
make ci
git status --short
```

두 번째 명령은 생성·테스트 과정이 추적 파일을 바꾸지 않았는지 확인하기 위한 것입니다.

## Phase 0 — 기반 구성

- [x] 모노레포 골격과 소유 경계가 있다: `test -f AGENTS.md && test -f go.work && test -f package.json`
- [x] 단일 OpenAPI 계약이 있다: `test -f openapi/jandibat.yaml && make openapi-lint`
- [x] Go API와 Yarn workspace가 독립 검사된다: `make test-api typecheck`
- [x] 로컬 CockroachDB 실행·migration 경로가 문서화돼 있다: `docker compose config --quiet && test -f docs/LOCAL_DEV_COCKROACH.ko.md`
- [x] 인증/연동 보안 검토 기준이 있다: `test -f docs/SECURITY_CHECKLIST.ko.md`
- [ ] 깨끗한 checkout에서 `make ci`가 통과한다.

## Phase 1 — Public Activity MVP

- [x] GitHub public activity adapter가 fixture 기반 계약 테스트와 실제 수집 실패 정책을 충족한다.
- [x] `GET /v1/activities/{subject}`가 기간·timezone·source 집계를 계약대로 반환하고 경계값 테스트가 통과한다.
- [x] `GET /v1/render/{subject}.svg`가 유효하고 안전한 SVG를 반환하며 theme/기간/error snapshot이 있다: `apps/api/internal/render/heatmap_test.go`, `apps/api/internal/http/contract_test.go`
- [x] 프론트엔드가 생성 타입 기반 client로 1년 heatmap, loading/empty/error 상태와 접근 가능한 tooltip을 제공한다.
- [x] README/블로그용 embed URL과 캐시 정책, 사용 예제가 문서화돼 있다.
- [x] API handler, domain, storage, frontend를 포함한 Phase 1 자동 인수 테스트와 API/SVG smoke가 통과한다.

검증 기준:

```sh
make ci
curl --fail --silent http://localhost:8080/healthz
curl --fail --silent 'http://localhost:8080/v1/activities/octocat' | jq -e '.subject == "octocat" and (.days | type == "array")'
curl --fail --silent 'http://localhost:8080/v1/render/octocat.svg' | xmllint --noout -
```

마지막 세 명령은 API를 실행한 상태에서 수행하며, 지원되는 fixture subject가 달라지면 문서와 함께 갱신합니다.

## Phase 2 — 인증과 연동

- [x] Magic Link 요청/소비가 단일 사용·만료·열거 방지·rate limit 테스트를 통과하고 실제 메일 adapter와 로컬 fake가 분리돼 있다.
- [x] Passkey 등록/인증 ceremony가 challenge/origin/RP ID/signature 검증과 replay 테스트를 통과한다: `apps/api/internal/auth/service_test.go`, `apps/api/internal/adapters/auth/webauthn/verifier_test.go`
- [x] OAuth/token 연결이 state/PKCE/redirect allowlist/최소 scope를 적용하고 암호화 저장·폐기 경로를 제공한다.
- [x] private activity는 인증·소유권을 검증하고 공개 cache/render 응답에 섞이지 않는다.
- [x] 프론트엔드에 Magic Link, Passkey, provider 연결/해제 흐름과 오류·재시도·접근성 상태가 있다.
- [ ] `SECURITY_CHECKLIST.ko.md`의 적용 항목이 PR 증거와 함께 검토됐다.

검증 기준:

```sh
make ci
go test ./apps/api/... -run 'MagicLink|Passkey|WebAuthn|OAuth|Authorization|Private'
```

추가로 브라우저 인수 테스트에서 신규 가입, 재로그인, 만료/replay, 연결/해제, 다른 사용자 리소스 접근 거부를 확인합니다.

## Phase 3 — 확장성과 운영성

- [x] Custom provider API/SDK가 versioned push-only schema, 분산 idempotency, quota, payload 제한과 URL 비수용 SSRF 경계를 제공한다.
- [x] GitLab과 Codeberg adapter가 동일한 contract suite와 rate-limit/error fixture를 통과한다.
- [x] 동기화 scheduler가 중복 실행 방지, retry/backoff/jitter, 실패 격리, 관측 가능한 상태를 제공한다.
- [x] token/key rotation, 데이터 보존·삭제, 감사 로그, 백업·복구 runbook이 있다.
- [ ] 부하·장애·보안 테스트의 합의된 SLO를 충족한다.
- [ ] 배포·rollback·migration 절차를 staging에서 검증했다.

로컬/CI 자동화 준비 상태(실제 staging 성공 증거와 구분):

- [x] migrated CockroachDB와 실제 `jandibat_api`/`jandibat_worker`/`jandibat_maintenance` DSN을 주입해 FK-free Magic Link delivery intent/crash/replay, purpose binding, Passkey ceremony/CAS, operations persistence, same-transaction mutation audit의 worker-role delivery/fence·intentional denial replay guard·400일 legal-hold retention, durable custom-ingest idempotency, subject resurrection 차단, async deletion enqueue와 분산 rate-limit integration test를 6개 package에서 skip 없이 실행한다: `make test-api-integration`, CI `migrations` job.
- [x] 30분 요청률 profile, p95/p99/error gate, 선택적 custom-ingest partition invariant와 JSON 결과 생성기가 있다: `scripts/load-check.mjs`.
- [x] CORS/preflight·CSRF·private cache/authz·magic replay·oversized/slow-body·log canary와 self-contained SVG smoke가 JSON 증거를 만들고, isolated fixture가 없으면 `NOT RUN`을 명시하며 staging gate를 실패시킨다: `scripts/security-smoke.mjs`.
- [x] staging 배포가 backup → checksum migration/재검증 → runtime role → smoke → 격리 restore verify를 자동화하고 artifact를 보존한다.
- [x] 직전 digest rollback/re-promotion과 안전장치가 있는 API/worker SIGTERM fault rehearsal 경로가 있다.
- [x] 보안 검토는 control ID별 상태·증거·검토자·UTC를 요구하며 미실행/실패 항목을 기계적으로 차단한다: `make security-review-check SECURITY_REVIEW=...`.

외부 staging이 없으므로 위 자동화의 실제 SLO 수치, 5%/25% traffic canary, provider/DB/network fault, DAST, RPO/RTO는 여전히 미검증입니다. `docs/evidence/staging/`에 동일 SHA/digest 실행 결과가 없으면 두 운영 체크박스를 완료하지 않습니다.
DB pool wait p99 histogram과 Cockroach retry-required signal은 구현됐습니다. 남은 계측 blocker는 외부 backup controller의 연속 실패/RPO 신호와 소유권·privacy 노출을 검출할 security analytics 신호입니다.

검증 기준:

```sh
make ci
go test ./apps/api/... -run 'Custom|GitLab|Codeberg|Scheduler|Retry|Idempotency|SSRF'
```

운영 항목은 자동 테스트 결과 링크, staging 실행 기록, runbook 링크가 모두 있어야 완료로 처리합니다.

## 완료 판정 규칙

- Phase는 해당 섹션의 모든 체크박스와 공통 품질 게이트가 완료돼야 완료입니다.
- 현재 구현과 맞지 않는 요구가 생기면 범위를 조용히 줄이지 말고 계획·OpenAPI·change log를 먼저 갱신합니다.
- 외부 서비스나 수동 검증이 필요한 항목은 실행 날짜, 환경, 증거 링크를 PR에 남깁니다.
- 일시적으로 skip된 테스트, placeholder 응답, mock만 있는 외부 연동은 완료 증거가 아닙니다.
