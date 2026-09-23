# Interface Change Log

API 계약 변경 시 이 파일과 GraphQL SDL(도메인) 또는 `openapi/jandibat.yaml`(HTTP edge)을 같은 변경에 포함합니다.
각 항목에는 날짜, 호환성, 영향받는 operation/schema, 백엔드·프론트엔드 후속 작업을 기록합니다.

## 2026-09-24 — GraphQL SDL 도메인 계약 도입

호환성: 최종 프로토콜 전환은 breaking change입니다. 이 단계에서는 신규 도메인 계약의
권위를 GraphQL SDL로 옮기고 gqlgen·Relay 생성 검사를 추가합니다. 기존 REST 도메인
operation은 공식 프론트엔드의 Relay 전환 전까지 임시로 유지하며, OpenAPI를 edge
endpoint로 축소하는 별도 cutover에서 제거합니다.

- GraphQL SDL은 도메인 query/mutation, Relay Node identity, ActivitySnapshot의 계약입니다.
- OpenAPI의 최종 범위는 health, SVG render, OAuth/magic-link callback, custom activity ingest입니다.
- Backend: gqlgen resolver를 기존 application port에 연결하고 authorization regression을 검증합니다.
- Frontend: Solid 2를 유지하며 pinned relay-runtime·solid-relay 어댑터 뒤의 normalized store로 전환합니다.
- Coordination: SDL·generated artifact drift를 CI에서 검사하고 REST 제거 시 이 로그를 갱신합니다.

## 2026-09-24 — Relay Node 전역 식별자

호환성: GraphQL 계약에 `node(id: ID!): Node`를 추가하는 additive 변경입니다. opaque ID는
버전과 엔터티 종류를 구분하며, 접근 불가와 삭제된 객체는 모두 `null`로 반환합니다.

- `Node` 구현은 Subject, ProviderConnection, CustomProvider, SyncJob, Session으로 제한합니다.
  activity day·통계·설정은 값 객체이며 전역 ID를 받지 않습니다.
- Backend: 유형별 조회는 application service/port를 사용하고 기존 공개/소유자 가시성을
  유지합니다. Session 조회는 사용자 ID와 세션 ID를 모두 조건으로 제한합니다.
- Frontend: Node ID는 불투명 값으로만 저장·전달하며 데이터베이스 ID로 파싱하지 않습니다.

## 2026-08-12 — 운영·보안 계약 강화 (`1.1.0`)

호환성: `1.0.0` 개발 기준선 대비 breaking change. 아직 배포되지 않은 계약의 Phase 3 완료 조건을 명시적으로 고정합니다.

- `synchronizeProviderConnection`과 `ingestCustomProviderActivities`에서 `Idempotency-Key`를 필수로 바꾸고 최소 24시간 보존 규약을 강제했습니다.
- `CustomActivityIngestRequest.schemaVersion`을 필수 상수 `1.0`으로 추가하고 batch-level 실패와 event-level 부분 거부의 경계를 문서화했습니다.
- OAuth2 연결 생성 응답은 `authorizationUrl`을 조건부 필수로 정의했습니다.
- OAuth2 연결 시작과 callback은 provider 브라우저 redirect가 재전송할 수 있는 동일한 유효 `jandibat_session` cookie를 필수로 하며, bearer-only 시작·세션 누락·불일치는 authorization code 교환 전에 401로 거부하도록 명시했습니다.
- Magic Link의 일회성 원문 토큰은 query string이 아니라 URL fragment에만 전달해 HTTP access log와 Referer에 노출되지 않도록 했습니다.
- 세션 IP는 port가 제거된 IPv4 또는 IPv6만 허용하도록 바로잡았습니다.
- Custom provider는 서버가 사용자 URL로 outbound 요청을 수행하지 않는 push-only 모델임을 명시했습니다. 따라서 임의 URL/redirect를 받지 않는 계약 자체가 SSRF 경계입니다.
- 모든 JSON request body operation에 공통 1 MiB 제한의 `413 Payload Too Large` 응답을 선언했습니다.
- 모든 operation에 cooperative deadline 및 필수 audit-intent 저장 실패가 반환할 수 있는 RFC 9457 `503 Service Unavailable`을 선언했습니다.
- Cursor/limit 및 path 식별자 검증에서 실제 반환되는 `400 Bad Request`를 session/subject/provider/custom/sync 조회·삭제 operation에 명시했습니다.
- cookie가 포함된 mutation에 공통 Origin 검증이 적용되므로 가능한 `403 Forbidden` CSRF 응답을 각 mutation 계약에 맞췄습니다.
- Activity metric 값은 집계 overflow와 남용을 막기 위해 event당 `0..1,000,000`으로 제한하고, custom ingest 초과값은 `metric_value_too_large` 부분 거부로 처리합니다.
- 익명 activity/SVG 조회의 선언된 429를 분당 IP 60회로 구현하고, 동일 provider fetch는 coalesce하며 전역 16/provider별 4개 동시 호출로 제한했습니다. 성공한 empty 외부 조회는 임의 subject/cache row를 만들지 않습니다.
- OpenAPI server URL은 localhost 고정값 대신 배포 환경의 HTTPS origin을 사용하는 상대 URL로 바꿨습니다.
- Passkey clone/sign-counter 의심은 세션을 발급하지 않고 401 Problem code `magic_link_reauthentication_required`로 거부하며 Magic Link 재인증을 요구하도록 명시했습니다.
- 저장소 SPDX 선언과 맞도록 API license identifier를 `AGPL-3.0-only`로 통일했습니다.
- Production runtime image는 API, sync worker, maintenance executable을 제공하며 각각 고정 DB username과 자기 DSN만 허용합니다. 배포 interface는 `/jandibat-api`, `/jandibat-worker`, `/jandibat-maintenance`와 `/livez`·`/readyz`·`/healthz` process health 경로를 사용합니다.
- 운영 표면 `GET /metrics`는 Prometheus text를 제공하고 ingress에서 monitoring network로 제한합니다. `/livez`는 process liveness, `/readyz`는 database/필수 dependency readiness이며 `/healthz`는 readiness 호환 alias입니다. 모든 metric sample은 `build_sha`, `environment`, `region` label을 포함하고 staging은 `BUILD_SHA`와 `REGION`을 명시 주입합니다. 운영 probe/metric은 공개 OpenAPI 계약에 포함하지 않습니다.
- OAuth callback `code`/`state`/`error`/`error_description`, provider scope, WebAuthn credential envelope와 custom-event metadata key에 명시적 길이·문자·property 상한을 추가했습니다. 이는 전역 body 제한과 별개인 입력별 방어입니다.
- Credential provider 연결의 private activity는 `includePrivate=true`라는 별도 owner consent 없이는 활성화하지 않습니다. 응답의 `privateDataEnabled`가 실제 동의 상태를 명시하며 `authMethod`에서 암묵적으로 추론하지 않습니다.
- `DELETE /v1/subjects/{subject}`는 동기 `204` 삭제에서 durable enqueue `202`로 변경됐습니다. 응답은 `{requestId,status:"requested"}`이고 API는 primary-data DELETE 권한을 갖지 않으며 maintenance executor가 lease-fenced workflow를 수행합니다.

구현 후속 작업:

- Backend: idempotency ledger, schema version 검증, event-level 부분 거부, OAuth authorization URL, IP 정규화를 구현합니다.
- Frontend: 모든 sync/ingest 요청에 UUID idempotency key와 schema version을 보내고 부분 거부를 표시합니다.
- Coordination: 생성 타입을 재생성하고 migration/contract CI에서 drift를 차단합니다.

## 2026-08-12 — Phase 0~3 계약 기준선 (`1.0.0`)

호환성: 기존 `0.1.0` 스캐폴드 대비 breaking change. 아직 배포되지 않은 개발 계약을 구현 기준선으로 확정합니다.

- Public Activity
  - `GET /v1/activities/{subject}`에 timezone, force refresh, 실패 정책, environment 필터를 정의했습니다.
  - 응답을 도메인 모델과 맞춰 `environments[]`, `days[].entries[]`, `generatedAt`, `stale`, 조회 기간을 포함하도록 변경했습니다.
  - `GET /v1/render/{subject}.svg`에 동일한 조회 옵션과 theme/cell/legend/week-start 렌더링 옵션을 정의했습니다.
  - Activity/SVG GET은 항상 non-destructive `keep_stale` read policy를 사용하며 `failurePolicy` query를 노출하지 않습니다. `purge`는 owner-authenticated sync mutation과 background scheduler 설정에서만 적용합니다.
  - 2026-08-12: 계약과 handler에 없는 `failurePolicy` query를 임베드 가이드가 지원하는 것처럼 설명하던 문서 오류를 정정했습니다. API 계약 변경은 없습니다.
- Authentication / sessions
  - Magic Link 요청·consume, Passkey 등록·sign-in의 options/finish ceremony를 모두 정의했습니다.
  - 현재 세션 조회/로그아웃, 세션 목록, 개별·일괄 revoke 계약을 추가했습니다.
  - 사용자 세션은 Secure/HttpOnly cookie 또는 opaque bearer token을 사용하며 WebAuthn credential payload는 JSON 직렬화 형태입니다.
- Subjects / settings
  - 사용자 프로필, 사용자 설정, subject 목록/생성/조회/수정/삭제 계약을 추가했습니다.
  - subject의 privacy, timezone, heatmap 기본값, scheduler, refresh 실패 정책을 별도 settings resource로 정의했습니다.
- Provider connections / sync
  - GitHub, GitLab, Codeberg catalog와 OAuth2/token/anonymous 연결 lifecycle을 정의했습니다.
  - 연결 상태, credential 갱신, 해제, 수동 sync enqueue, sync job 조회, OAuth callback을 추가했습니다.
  - 토큰은 write-only이며 sync 결과는 비동기 job 상태와 수집/거부 fact 수로 관찰합니다.
  - `authMethod=token` 연결 생성 request는 non-empty write-only `token`을 조건부 필수로 검증합니다.
  - 연결 해제는 local credential/facts/sync jobs를 한 transaction에서 제거하고 encrypted provider-token revoke job을 durably enqueue합니다. Provider-side revoke는 private key를 가진 worker가 lease/CAS로 재시도하며 API는 저장 token을 복호화할 수 없습니다.
- Custom providers
  - Subject-scoped custom provider CRUD, ingestion key rotation, API-key 기반 batch activity ingest를 추가했습니다.
  - `eventId` 기반 provider-scoped deduplication, batch 제한, 부분 거부 결과를 명세했습니다.
  - Create/rotate response의 one-time `ingestionKey`는 required read-only response field로 명시하고 이후 조회 응답에서는 제외합니다.
- 공통 규약
  - 인증 방식을 `sessionCookie`, `bearerAuth`, `customProviderKey`로 구분했습니다.
  - 모든 오류를 RFC 9457 `application/problem+json` 및 stable `code`/`requestId` 구조로 통일했습니다.
  - pagination, idempotency key, request ID, ETag, Location, retry headers를 공통 components로 정의했습니다.
  - 모든 contract mutation은 durable audit intent를 기록하기 전에 shared IP/session abuse bucket을 통과하며 초과 시 `429`와 `Retry-After`를 반환합니다. Unmatched mutation path는 durable audit row를 만들지 않습니다.

구현 후속 작업:

- Backend: operationId별 handler/application/storage/provider adapter를 구현하고 민감 credential을 응답에서 제외합니다.
- Frontend: OpenAPI 타입을 재생성하고 기존 `sources[]` 기반 heatmap을 `environments[] + entries[]` projection으로 전환합니다.
- Coordination: OpenAPI lint와 generated-type drift 검사를 CI 필수 gate로 추가합니다.

## 2026-03-02 — Initial scaffold (`0.1.0`)

- Initial OpenAPI scaffold added (`openapi/jandibat.yaml`).
