# Interface Change Log

API 계약 변경 시 이 파일과 GraphQL SDL(도메인) 또는 `openapi/jandibat.yaml`(HTTP edge)을 같은 변경에 포함합니다.
각 항목에는 날짜, 호환성, 영향받는 operation/schema, 백엔드·프론트엔드 후속 작업을 기록합니다.

## 2026-09-24 — CustomProvider HTTP 수집 식별자

호환성: GraphQL `CustomProvider`에 owner-scoped `ingestProviderID: String!`을 추가하는
additive 변경입니다. `id`는 계속 불투명 Relay Node ID이며, 클라이언트는 이를 해독하지
않습니다. `ingestProviderID`는 유지되는 HTTP
`POST /v1/custom-providers/{customProviderId}/activities:ingest`의 경로 UUID입니다.

- 이 식별자는 수집 credential이 아닙니다. 일회용 `ingestionKey`는 Node나 Relay normalized
  store, AppState, localStorage에 저장하지 않고 수집 요청 헤더에만 사용합니다.
- Backend: owner에게만 반환되는 CustomProvider 투영에서 저장된 UUID를 매핑합니다.
- Frontend: HTTP 수집 경로에는 `ingestProviderID`를 사용하고 Relay `id`는 Node 조작에만
  사용합니다. key 표시·전달은 ephemeral 경계에서 처리합니다.
- Coordination: SDL/gqlgen/Relay 생성물과 계약 테스트의 drift를 검증합니다.

## 2026-09-24 — GraphQL HTTP transport 공개 경계

호환성: 기존 REST 도메인 경로를 제거하기 전 `POST /graphql`을 additive로 추가합니다.
GraphQL SDL만 도메인 계약이며 이 transport 경로는 OpenAPI 도메인 스키마로 중복
정의하지 않습니다. 아직 운영 Flux reconcile은 하지 않았습니다.

- JSON 단일 POST 요청만 받습니다. GET, WebSocket, batch, 초과 본문과 과도한
  깊이·필드·비용의 작업을 거부합니다. 운영 요청은 이름을 가져야 하며 introspection은
  명시적 개발 모드에서만 허용합니다. Subscription은 없습니다.
- 검증된 bearer 또는 쿠키 세션을 사용하고 잘못된 자격 증명을 익명으로 낮추지 않습니다.
  쿠키 사용 요청에는 CSRF Origin 검사를 적용하며 OAuth 시작은 같은 쿠키 세션에 state를
  묶습니다. 로그인 토큰은 GraphQL 응답이 아니라 HttpOnly cookie에만 씁니다.
- mutation은 응답에서 선택한 필드와 독립적으로 typed 오류·실행 오류를 감사 결과에
  반영합니다. 인증 후 계정별·민감 작업별 분산 제한과 입력 비노출 로그·고정 카디널리티
  메트릭을 적용합니다. 일회용 수집 키가 있는 응답을 포함해 cache-control은 no-store입니다.
- 기존 REST 도메인 경로는 공식 Solid 2 Relay UI 전환·OpenAPI cutover까지 임시로
  유지합니다. OAuth/magic-link callback, SVG, health, custom ingest는 HTTP edge에 남습니다.

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

## 2026-09-24 — 정적 ActivitySnapshot 계약

호환성: GraphQL 도메인 계약에 조회 전용 `subject(handleOrID).activitySnapshot`을 추가하는
additive 변경입니다. 범위는 양 끝 날짜를 포함하고 timezone은 IANA 이름이어야 합니다.

`subject(handleOrID:)`는 원시 handle 또는 local ID를 받습니다. 전역 Relay ID 조회는
`node(id:)`만 사용해 유효한 handle과 base64url ID 사이의 충돌을 피합니다. 기존
Subject 조회 저장소와 같이 handle이 다른 Subject의 local ID와 같으면 ID가 우선하며,
해당 handle의 Subject는 `node(id:)`로 조회할 수 있습니다.

- `ActivityDay`, entry, environment, 통계는 값 객체이며 Relay Node가 아닙니다.
- count·metric·total은 데이터베이스 `INT8` 범위를 잃지 않도록 10진 문자열 `Long` scalar로
  직렬화합니다. heatmap level과 최장 streak만 범위가 작은 `Int`입니다.
- `generatedAt`은 일관된 조회 뒤의 서버 UTC 시각, `revision`은 조회 범위·독자 범위·결과가
  같으면 안정적인 불투명 값입니다. `dataUpdatedAt` 필드는 항상 응답에 있으며, 해당
  범위에 데이터가 한 번도 반영되지 않았으면 `null`입니다.
- Backend: 공개·소유자·인증된 비소유자 범위를 구분하고, 실제 가시 데이터 변경만 영속
  마커에 반영합니다. GraphQL resolver는 비공개 Subject 접근 실패와 부재를 구별하지 않습니다.
- Frontend: 정적 소비자는 subscription 대신 주기적으로 재조회하고 revision 불변 시
  산출물 교체를 생략할 수 있습니다. 공식 UI는 이후 Relay store로 전환합니다.

## 2026-09-24 — 세션·동기화 작업 Relay connection

호환성: GraphQL 도메인 계약에 `viewer.sessions`와
`ProviderConnection.syncJobs`를 추가하는 additive 변경입니다. 두 목록은
`(createdAt, id)` 위치의 종류별 불투명 커서를 사용하고 최대 `first=100`으로 제한합니다.
삭제된 커서 기준 행은 재조회하지 않으며, 같은 시각의 행을 건너뛰거나 중복하지 않습니다.

- Session은 소유자만 볼 수 있고 bearer token·token hash를 노출하지 않습니다.
- SyncJob은 부모 ProviderConnection 소유권을 확인한 뒤에만 조회합니다.
- 부모 연결이 조회 사이에 삭제·철회되거나 접근 불가가 되면 `syncJobs`는 `null`이며,
  내부 저장소 오류와는 구별합니다.
- Node는 기존 다섯 durable entity로 유지하고 edge·PageInfo·Viewer는 값 객체입니다.
- Backend: Query/field resolver를 application port에 연결하고 소유자·비소유자·익명
  보안 테스트를 추가합니다. operation-aware 감사는 위 HTTP transport 항목에서 완료했습니다.
- Frontend: 생성된 connection artifact를 사용하고 cursor를 해석하거나 DB offset으로
  바꾸지 않습니다.

## 2026-09-24 — 인증 mutation의 GraphQL 계약

호환성: 기존 REST 도메인 인증 operation의 GraphQL 대체를 추가하는 additive 전환입니다.
Magic Link 소비는 브라우저 callback HTTP edge에 남기고, 요청은 GraphQL mutation으로
옮깁니다. Passkey ceremony 시작·완료와 세션 철회는 typed payload를 반환합니다.

- credential JSON은 mutation 변수로만 전달하며 Node 필드·오류·로그에 복제하지 않습니다.
  WebAuthn 옵션 JSON은 구조화된 문자열로 반환하고, HTTP 경계에서 크기·형식을 검증합니다.
- 로그인 성공 시 raw session token은 GraphQL 응답에 넣지 않고 신뢰된 HTTP transport가
  HttpOnly cookie로 전달합니다. `revokeOtherSessions`는 bearer token 대신 검증된 현재
  Session ID를 사용합니다.
- 오류 payload는 입력·도메인 오류에 한정하고 인증·transport·내부 실패는 GraphQL 오류로
  처리합니다. 위 HTTP transport 항목의 감사·CSRF·cookie 경계를 통과한 뒤
  `POST /graphql`에 연결했습니다. 운영 reconcile은 하지 않았습니다.

## 2026-09-24 — Subject·설정 GraphQL 계약

호환성: GraphQL 도메인 계약에 `viewer.subjects`, 공개/소유자 Subject 프로필,
사용자·Subject 설정과 typed 변경 payload를 추가합니다. `Subject`만 Node이며 설정,
connection edge와 삭제 요청은 값 객체입니다.

- 목록은 `(createdAt,id)`의 종류별 불투명 커서를 사용하고 `first=1..100`으로 제한합니다.
- Subject 설정은 소유자에게만 보이며 공개 Subject의 비소유자는 `settings: null`입니다.
- `updateSubject`에서 `clearDisplayName`은 명시적 삭제를 뜻하고 새 값과 동시에 지정할
  수 없습니다. GraphQL의 생략/null 차이를 암묵적으로 DB 변경에 사용하지 않습니다.
- 삭제는 즉시 Node를 지우지 않고 durable 삭제 요청의 ID·상태를 반환합니다.
- Backend: 검증된 사용자 ID·Subject 소유권을 각 resolver에서 재검사하고 기존
  application service 및 삭제 workflow만 호출합니다. HTTP transport의 공통
  보안 경계를 거쳐 mutation을 노출합니다.

## 2026-09-24 — 연동 조회 GraphQL 계약

호환성: `providerCatalog`는 내장 3종만 반환하고, Subject의 provider connection·custom
provider 목록은 소유자 전용 nullable Relay connection으로 노출합니다. 비소유자는 목록을
열람할 수 없으며 각 목록은 서로 다른 `(createdAt,id)` 불투명 커서와 `first=1..100`을
사용합니다. Node에는 공개 메타데이터만 두고 credential, ingest hash, 외부 로그인명과
원문 제공자 오류는 노출하지 않습니다. OAuth callback과 custom ingest는 HTTP edge에
남습니다.

## 2026-09-24 — Provider connection 변경 GraphQL 계약

호환성: provider 연결 생성·수정·철회와 수동 동기화 요청을 typed mutation payload로
추가합니다. TOKEN 비밀과 idempotency key는 입력으로만 받고 응답·Node에 복제하지
않습니다. OAuth 시작은 검증된 쿠키 세션 바인딩과 redirect allowlist를 요구하며 callback은
HTTP edge에 남습니다. 철회된 연결은 Node에서 사라지므로 철회 결과는 클라이언트의
normalized store 축출에 필요한 Relay ID만 돌려줍니다. 동기화는 기존 24시간
idempotency 의미를 유지합니다. 위 HTTP transport 항목에서 operation별 rate limit,
CSRF 및 감사 경계를 완료한 뒤 `POST /graphql`에 연결했습니다.

## 2026-09-24 — Custom provider 변경 GraphQL 계약

호환성: custom provider 생성·변경·수집 키 회전·삭제를 typed mutation으로 옮깁니다.
생성·회전 응답의 `ingestionKey`는 한 번만 보여 주는 payload 필드이며 Node·조회·로그·
Relay normalized record에 저장하지 않습니다. `CustomProvider` Node는 안전한 설정
메타데이터만 노출합니다. 삭제 결과는 normalized store 축출에 필요한 Relay ID를
반환합니다. 수집 이벤트 전송은 HTTP edge에 남습니다. 위 HTTP transport 항목의
CSRF·operation별 rate limit·민감 응답 no-store를 거쳐 mutation을 노출합니다.

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
