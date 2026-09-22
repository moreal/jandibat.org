# 플랫폼 현대화 설계

- 상태: 채택(구현 대기)
- 결정일: 2026-09-22
- 범위: API 계약, Solid 프론트엔드 데이터 계층, Go 데이터 접근과 정적 분석,
  재현 가능한 개발 환경, Kubernetes 배포와 백업

## 1. 목표와 허용 범위

이번 작업은 기존 인터페이스와 데이터를 보존하는 점진적 이전이 아니다. 다음 변경을 명시적으로
허용한다.

- Solid 2는 필수이며 Solid 1로 되돌리지 않는다.
- 기존 CockroachDB 데이터와 migration history를 폐기한다.
- OpenAPI와 REST API의 하위 호환성을 보장하지 않는다.
- 전환 중 테스트가 일시적으로 실패할 수 있지만 각 구현 계획의 종료 시점에는 새 계약 기준으로
  전체 검증이 통과해야 한다.
- 운영 배포 전까지 이전 데이터 복원 경로는 만들지 않는다.

최종 상태는 다음과 같다.

```text
Solid 2 + solid-relay
        │ generated Relay artifacts
        ▼
Relay-compatible GraphQL ──────► Go application/domain ports
        │                              │
        │                              ▼
        │                       Scythe generated pgx
        │                              │
        ▼                              ▼
소수의 HTTP edge endpoint          CockroachDB
(health/SVG/callback/ingest)       single-node on Talos
                                           │
                                           ▼
                                S3-compatible off-site backup
```

## 2. 계약 경계

`openapi/jandibat.yaml` 하나를 모든 API의 단일 계약으로 삼던 규칙을 프로토콜별 계약으로
바꾼다.

| 프로토콜 | 기준 계약 | 대상 |
| --- | --- | --- |
| GraphQL | `graphql/schema/**/*.graphqls` | 공식 웹 UI와 외부 데이터 소비자용 query/mutation |
| HTTP | `openapi/jandibat.yaml` | health, SVG, OAuth callback, custom activity ingest |

GraphQL 서버는 schema-first code generation을 제공하는 gqlgen을 사용한다. GraphQL schema와
OpenAPI 중 어느 쪽에도 같은 동작을 중복 노출하지 않는다. 계약 변경은 해당 schema와
`docs/interface-change-log.md`를 같은 변경에 포함한다.

일반 HTTP로 남기는 endpoint는 다음과 같다.

- `GET /healthz`와 readiness/metrics endpoint
- Markdown과 HTML에서 직접 참조하는 SVG render endpoint
- 브라우저 redirect가 필요한 OAuth callback과 magic-link consume
- API key와 idempotency header를 사용하는 custom activity ingest

나머지 사용자, Subject, 설정, provider connection, sync job 읽기와 변경은 GraphQL로 옮긴다.

## 3. Relay 적용 범위

공식 Solid 2 프론트엔드는 `relay-runtime`과 `solid-relay`를 사용한다. `solid-relay`는 아직
규모가 작은 개발 단계의 통합체이므로 정확한 Git revision으로 고정하고, 별도의
`apps/web/src/relay/**` 어댑터 뒤에 둔다. Solid 2 및 현재 Relay compiler와 맞지 않는 부분이
확인되면 최소 패치를 별도 package로 유지하되 upstream 반영 가능성을 보존한다.

Relay의 전역 identity는 수명이 있는 엔터티에만 부여한다.

- `Subject`
- `ProviderConnection`
- `CustomProvider`
- `SyncJob`
- `Session`

activity day, 통계, 설정 값은 `Node`가 아닌 값 객체다. 목록이 계속 증가하는 sessions와
sync jobs에는 Cursor Connections 규격을 적용하고, provider catalog처럼 크기가 제한된 목록은
일반 배열로 반환한다.

초기 버전에는 GraphQL subscription을 넣지 않는다. 예상 외부 소비자는 정적 생성기이며,
공식 UI의 sync 완료도 mutation 응답과 제한적인 refetch로 해결할 수 있다. 실시간 대시보드
요구가 생길 때 `subjectActivityUpdated`와 `syncJobUpdated`를 별도 ADR로 설계한다.

## 4. 정적 Activity snapshot

외부 GraphQL API의 우선 소비 형태는 정적 사이트 빌드, CLI, SVG/Canvas 생성기가 한 번
조회하는 재현 가능한 snapshot이다.

```graphql
type ActivitySnapshot {
  subject: Subject!
  range: DateRange!
  days: [ActivityDay!]!
  total: Int!
  longestStreak: Int!
  generatedAt: DateTime!
  dataUpdatedAt: DateTime
  revision: String!
}
```

- `generatedAt`: API가 일관된 snapshot을 계산한 서버 시각. UTC RFC 3339로 직렬화한다.
- `dataUpdatedAt`: snapshot에 반영된 Subject 데이터가 마지막으로 변경된 서버 시각.
- `revision`: 조회 범위, 공개 범위, timezone과 반영 데이터가 같으면 동일한 불투명 값.

`generatedAt`은 클라이언트의 로컬 시계로 대체하지 않는다. 외부 활용 가이드는 생성 결과에
`generatedAt`을 표시하고, 신선도가 중요한 경우 `dataUpdatedAt`도 표시하도록 권장한다.
보이는 문구를 생략해도 SVG `<metadata>`, HTML metadata 또는 JSON sidecar에는 세 값을 보존한다.
정기 갱신은 subscription 대신 CI cron, 정적 사이트 rebuild 또는 외부 scheduler로 수행한다.

## 5. CockroachDB와 Scythe

기존 `db/migrations/0001_*.sql`부터 `0013_*.sql`까지를 폐기하고 현재 요구사항을 반영한 단일
baseline migration으로 다시 작성한다. 배포 시 기존 PVC와 database는 삭제하고 새 cluster에만
baseline을 적용한다. 이 파괴적 전환은 staging 및 production 배포 전에 다시 명시적으로
확인한다.

SQL 접근은 Scythe가 생성한 Go pgx 코드를 기본으로 한다.

1. 공식 원본 Scythe의 고정 버전을 Nix로 제공한다.
2. baseline schema와 대표 query로 `cockroachdb` engine 및 Go pgx backend를 검증한다.
3. 실제 CockroachDB에 대해 generate/check/live verification을 통과시킨다.
4. 공식 구현의 구체적인 결함이 재현될 때만 `nix/patches/scythe-cockroach.patch`를 추가한다.
5. 패치에는 최소 재현 fixture와 upstream issue/PR 링크를 함께 둔다.

Scythe parser가 읽는 schema를 별도로 손으로 복제하지 않는다. Cockroach 전용 DDL 때문에
projection이 꼭 필요하다면 baseline에서 결정적으로 생성하고 drift 검사를 둔다. 정적 query는
`apps/api/internal/adapters/**/queries/*.sql`로 옮긴다. 동적 필터는 먼저 nullable parameter,
array parameter, 유한한 query variant로 표현하고, 테이블/열 이름이 런타임에 바뀌는 운영 query만
검증된 식별자 allowlist와 수동 scan을 유지한다.

연결 계층은 `database/sql` + pgx stdlib에서 `pgxpool`/`pgx.Tx`로 통일한다. query generation과
transaction retry는 분리한다. CockroachDB SQLSTATE `40001` retry는 application transaction
boundary에서 처리하고, 외부 네트워크 호출을 retry transaction 안에 넣지 않는다.

## 6. Go 타입·정적 분석·로깅

Go 언어의 enum/sum-type 한계를 다음 도구로 보강한다.

- `go vet ./...`
- `staticcheck ./...`
- `exhaustive -check=switch,map ./...`로 named constant enum 검사
- `go-check-sumtype ./...`로 sealed interface type switch 검사

sum type은 package-private marker method가 있는 interface와 같은 package의 variant로 제한한다.
의도적으로 일부 variant를 무시하는 switch는 빈 `default`로 숨기지 않고 도구별 명시적 ignore와
근거를 사용한다. 생성 코드는 필요한 경우 도구 설정에서 제외하되 handwritten resolver와 domain은
제외하지 않는다.

Go logging은 표준 `log` 기반 문자열 조립을 Zap으로 교체한다.

- production: JSON encoder
- local/test: 필요 시 console encoder 또는 observer core
- 고정 필드: `service`, `build_sha`, `environment`, `region`, `event`
- request 필드: `request_id`, method/operation, status, duration
- credential, authorization header, database URL password는 field 생성 전에 redaction
- 전역 logger 대신 composition root에서 `*zap.Logger`를 주입
- process 종료 전에 `Sync`하되 지원하지 않는 stderr/stdout sync 오류는 구분 처리

## 7. Nix와 버전 정책

루트 `flake.nix`와 `flake.lock`을 로컬 및 CI 도구 버전의 단일 진실 원천으로 사용한다.
Makefile은 사용자가 기억할 안정적인 명령 표면으로 남기며 `nix develop --command make <target>`로
실행한다. CI도 setup-go/setup-node/corepack의 별도 버전 pin 대신 Nix dev shell을 사용한다.

오래된 Yarn 1용 `yarn2nix`는 사용하지 않는다. nixpkgs의 `yarn-berry_4`,
`fetchYarnBerryDeps`, `yarnBerryConfigHook`으로 Yarn 4 offline cache와 build를 구성한다.

2026-09-22 기준 목표 버전은 다음과 같다.

| 도구 | 목표 |
| --- | --- |
| Go | 1.27.1 |
| Node.js | 24.21.0 LTS |
| Yarn | 4.18.0 |
| TypeScript | 7.0.2 |
| Vite | 8.3.0 |
| Vitest | 5.0.1 |
| Solid | 2.0.0-rc.9 계열, 정확한 동일 RC로 관련 package 고정 |

Solid는 반드시 2.x를 유지한다. RC package 간 version skew를 허용하지 않는다. 각 major upgrade는
lockfile, generated code, 타입 검사, 단위/통합 테스트와 production image build가 함께 통과해야
완료다.

## 8. Kubernetes 배포

배포 선언의 소유자는 `../homelab`이다. `jandibat.org` 저장소는 production image와 runtime
configuration contract, migration/backup 도구를 소유한다.

Talos cluster는 한 물리 호스트에 있으므로 CockroachDB replica를 여러 Pod로 늘려도 물리 장애를
견디지 못한다. 초기에는 공식 CockroachDB Helm chart의 StatefulSet mode로 secure single-node를
배포한다. 3개 이상의 독립 failure domain이 생기기 전에는 다중 node cluster로 가장하지 않는다.

`services/jandibat/k8s`에는 별도 Flux Kustomization 경계로 다음을 둔다.

- namespace와 NetworkPolicy
- secure single-node CockroachDB HelmRelease 및 local-path PVC
- API, worker, maintenance, web Deployment/Service
- migration Job
- SOPS-encrypted application/backup secrets
- image policy/update automation
- ServiceMonitor 또는 현재 Prometheus agent가 발견할 수 있는 scrape 설정
- PodDisruptionBudget 대신 single-node 중단 특성을 명시한 runbook

외부 공개는 homelab inventory의 ingress route가 소유한다. DB Console과 SQL port는 외부에
노출하지 않는다.

## 9. Provider-neutral S3 backup

CockroachDB native `BACKUP`과 external connection을 사용하며 PVC snapshot을 database backup으로
간주하지 않는다.

- RPO: 1시간
- RTO: incident 선언부터 검증된 복구까지 4시간
- 매일 00:10 UTC full backup
- 매시간 10분 incremental backup
- 보존 기간 35일
- bucket versioning/object lock 또는 동등한 immutability
- 매주 격리 schema/smoke restore, 매월 전체 restore drill

설정은 provider-neutral secret key로 표현한다.

```text
S3_ENDPOINT
S3_REGION
S3_BUCKET
S3_PREFIX
S3_ACCESS_KEY_ID
S3_SECRET_ACCESS_KEY
S3_FORCE_PATH_STYLE
BACKUP_RETENTION_DAYS
```

초기 가격 선택은 Backblaze B2와 Cloudflare R2 중 운영 시점 가격을 다시 비교해 결정한다. 설계는
AWS hostname이나 provider IAM에 의존하지 않는다. R2의 bucket lock과 B2의 S3 Object Lock처럼
immutability 의미가 다르므로 실제 provider 선택 시 restore/delete 실험을 acceptance evidence로
남긴다.

backup schedule은 CockroachDB `CREATE SCHEDULE FOR BACKUP` 하나만 authoritative scheduler로
사용한다. `updates_cluster_last_backup_time_metric`을 활성화하고 Prometheus에서 마지막 성공이
1시간을 초과하거나 연속 실패한 경우 alert한다. credential은 SOPS로 암호화하고 application
credential과 분리한다.

## 10. 완료 조건

- Nix shell과 CI가 같은 toolchain으로 전체 검증을 수행한다.
- GraphQL SDL 및 남은 OpenAPI가 구현과 generated artifact에서 drift하지 않는다.
- 공식 프론트엔드가 Relay store를 사용하고 수동 전역 서버 상태를 제거한다.
- 정적 ActivitySnapshot이 세 provenance field를 항상 반환하고 활용 가이드가 이를 설명한다.
- 새 빈 CockroachDB에 baseline migration과 모든 Scythe query가 적용·검증된다.
- staticcheck, exhaustive, go-check-sumtype, Zap logging 검증이 CI에 포함된다.
- Talos에서 web/API/worker/CockroachDB가 기동되고 DB port는 cluster 밖에 노출되지 않는다.
- off-site backup과 격리 restore evidence가 RPO/RTO 목표를 만족한다.

## 11. 의도적으로 제외한 것

- 기존 CockroachDB 데이터 이전
- GraphQL subscription과 실시간 공개 API
- 여러 물리 failure domain이 없는 상태에서의 CockroachDB HA 주장
- provider 전용 backup 구현
- 모든 SQL을 억지로 code generation 대상으로 바꾸는 것
- GraphQL로 SVG, OAuth callback, health, ingest를 감싸는 것
