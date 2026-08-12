# 서비스 수준 목표(SLO)

기준일: 2026-08-13

이 문서는 Phase 3 운영 검증에 사용할 수치, 측정 방법, 부하·장애·보안 게이트를 정의합니다. 이 수치는 목표이며 아직 staging에서 검증되지 않았습니다. 실제 측정 링크가 `docs/evidence/staging/`에 기록되기 전에는 달성으로 표시하지 않습니다.

## 1. 범위와 공통 정의

- 측정 환경: production과 동일한 이미지, CockroachDB major version, migration, secret 주입 방식, replica 수를 사용하는 staging.
- 측정 창: availability와 latency는 최근 30일 rolling window. 배포 전 시험은 별도의 30분 부하 창을 사용합니다.
- eligible request: 라우터까지 도달한 요청 중 운영자가 시작한 점검 트래픽을 제외한 요청.
- good request: deadline 안에 계약상 성공 응답을 반환한 eligible request.
- 서버 실패: `5xx`, handler timeout, process 또는 proxy에서 발생한 upstream reset.
- `4xx`는 availability 분자와 분모에서 제외하되, 서버가 잘못 분류한 오류와 rate-limit 오구성은 제외하지 않습니다.
- 계획 점검도 사용자 요청을 실패시키면 error budget을 소비합니다.
- 지연 시간은 load balancer가 관찰한 end-to-end 시간으로 측정하고, 내부 handler 시간은 진단 지표로만 사용합니다.

## 2. 사용자 요청 SLO

| 서비스 표면 | SLI | 30일 SLO | 단일 배포 gate |
| --- | --- | --- | --- |
| `GET /healthz` | 응답 성공률 | 99.99%, p95 100ms 이하 | 5분 연속 성공, 실패 0회 |
| public activity/cache hit | good request / eligible request | 99.90%, p95 300ms, p99 800ms | p95 300ms, p99 800ms, `5xx` 0.1% 이하 |
| SVG render/cache hit | good request / eligible request | 99.90%, p95 500ms, p99 1.2s | p95 500ms, p99 1.2s, `5xx` 0.1% 이하 |
| 인증·subject·provider mutation | good request / eligible request | 99.90%, p95 500ms, p99 1.5s | `5xx` 0.1% 이하, timeout 0회 |
| custom provider ingest | accepted 또는 계약상 duplicate 응답 / eligible request | 99.90%, p95 700ms, p99 2s | 아래 ingestion 부하에서 데이터 유실 0건 |
| sync enqueue/job 조회 | good request / eligible request | 99.90%, p95 500ms, p99 1s | enqueue 중복 0건, 고아 running job 0건 |

외부 provider의 실제 장애는 jandibat API availability에서 분리해 `dependency_error`로 계측합니다. 다만 외부 장애를 안정된 계약 오류로 변환하지 못하거나, 다른 provider 요청까지 실패시키거나, stale 정책을 위반하면 jandibat 실패로 계산합니다.

## 3. 비동기 처리와 데이터 SLO

| 항목 | 목표 |
| --- | --- |
| scheduled sync 시작 지연 | 예정 시각 대비 p95 5분 이하, p99 15분 이하 |
| public/private activity freshness | 정상 provider에서 95%가 30분 이내, 99%가 2시간 이내 |
| retry queue | oldest ready job age p95 5분 이하, 30분 초과 job 0건 |
| 중복 실행 | 동일 connection의 동시 실행 0건 |
| ingestion idempotency | 동일 `(provider, eventId)` 재전송으로 생성되는 추가 fact 0건 |
| 감사 이벤트 | critical mutation 유실 0건, 영속화 지연 p99 60초 이하 |
| 삭제 요청 | primary DB 삭제 7일 이내, backup 만료 35일 이내 |
| 재해 복구 | RPO 1시간 이하, RTO 4시간 이하 |

## 4. 필수 계측

다음 metric 또는 의미가 동일한 metric이 없으면 SLO를 검증할 수 없습니다.

- `http_server_requests_total{route,method,status_class}`
- `http_server_request_duration_seconds{route,method}` histogram
- `provider_requests_total{provider,outcome}`와 `provider_request_duration_seconds`
- `sync_jobs_total{provider,status,trigger}`
- `provider_token_revocation_dead_jobs`와 `provider_token_revocation_oldest_dead_seconds`
- `sync_queue_oldest_ready_seconds`와 `sync_job_start_delay_seconds`
- `activity_freshness_seconds{provider,visibility}`
- `custom_ingest_events_total{outcome}`
- `audit_events_total{action,outcome}`와 `audit_persist_delay_seconds`
- `retention_rows_total{table,outcome}`와 `deletion_request_age_seconds`
- `credential_decrypt_operations_total{key_id,outcome}`
- `db_pool_in_use`, `db_pool_wait_seconds`, `db_pool_wait_duration_seconds` histogram, CockroachDB transaction retry/error 수
- build SHA, environment, region을 모든 metric과 structured log에 연결하는 resource label

API의 `/readyz`와 호환 경로 `/healthz`는 DB 연결과 필수 runtime dependency를 함께 검사하고 `/livez`는 process liveness만 검사합니다. Sync worker와 maintenance도 각각 내부 health port에서 같은 세 경로를 제공하며, readiness 503인 process는 재시작/격리하고 신규 작업을 배정하지 않습니다. Web container의 `/healthz`는 정적 서버 liveness를 검사합니다. Staging Compose는 API/Web만 loopback host port에 publish하고 worker/maintenance health는 container 내부 probe로 확인합니다.

Prometheus recording/alert rules는 `deploy/monitoring/prometheus-rules.yaml`, 운영 dashboard는 `deploy/monitoring/grafana-dashboard.json`을 기준으로 provisioning합니다. 규칙은 4xx를 eligible request에서 제외하고 environment/region/route별 burn을 계산합니다. `make monitoring-check`는 artifact 구문을 검사하며 실제 Prometheus datasource에서 rule evaluation과 dashboard screenshot을 staging 증거에 남겨야 합니다.

현재 rule은 burn/queue/revocation DLQ 외에도 audit persistence 실패, freshness 30분/2시간 비율, 7일 초과 deletion request, credential decrypt 실패와 Cockroach `retry_required` 최종 오류를 경보합니다. Worker와 maintenance는 실제 복호화에 성공하거나 실패한 설정 key ID를 `credential_decrypt_operations_total`로 기록하고, 저장된 미등록 ID는 `unknown`으로 축약하므로 구 key canary의 숨은 consumer를 확인할 수 있습니다. 반면 backup 연속 실패/RPO 초과는 이 repository runtime이 metric을 emit하지 않고, 소유권·privacy 노출은 신뢰 가능한 runtime signal이나 security analytics pipeline이 없습니다. 두 page 조건은 외부 backup controller/보안 분석 계측이 연결되기 전 `NOT IMPLEMENTED`이며 Phase 3 경보 완료를 차단합니다.

Runtime은 bounded `pgxpool` checkout을 `AcquireTracer`로 관측해 `db_pool_wait_duration_seconds` histogram을 emit합니다. DB pool wait p99는 `histogram_quantile(0.99, sum by (le,environment,region) (rate(db_pool_wait_duration_seconds_bucket[5m])))`로 계산하며 누적 `db_pool_wait_seconds`를 p99로 오해하지 않습니다. SQLSTATE `40001` 최종 실패는 `cockroach_transaction_errors_total{outcome="retry_required"}`로 관측합니다.

## 5. Error budget과 경보

99.90% monthly SLO의 budget은 30일 기준 약 43분 49초입니다. route별 traffic 차이가 있으므로 시간만이 아니라 request-based error ratio도 함께 봅니다.

- page: 최근 1시간 burn rate가 14.4배 이상이고 최근 5분도 14.4배 이상.
- page: 최근 6시간 burn rate가 6배 이상이고 최근 30분도 6배 이상.
- ticket: 최근 3일 burn rate가 1배 이상.
- 즉시 page: audit critical event 유실, 잘못된 소유권 데이터 노출, backup 연속 2회 실패, RPO 1시간 초과, queue age 30분 초과.
- 즉시 page: OAuth provider token revocation job이 `dead`로 최초 전이하거나 기존 dead job 수가 증가.
- 신규 배포 중 page 조건이 5분 지속되면 자동 promotion을 중단하고 rollback 판단을 시작합니다.

## 6. 배포 전 부하 시험

저장소 기본 runner는 추가 도구 설치 없이 동일한 요청률·duration·latency/error/ingestion partition gate를 실행합니다.

```sh
LOAD_BASE_URL="$STAGING_BASE_URL" \
LOAD_DURATION_SECONDS=1800 \
LOAD_ACTIVITY_RPS=50 \
LOAD_RENDER_RPS=10 \
LOAD_INGEST_RPS=20 \
LOAD_INGEST_EVENTS_PER_REQUEST=100 \
LOAD_INGEST_PROVIDER_ID="$STAGING_CUSTOM_PROVIDER_ID" \
LOAD_INGEST_PROVIDER_KEY="$STAGING_CUSTOM_PROVIDER_KEY" \
LOAD_OUTPUT_FILE=load-result.json \
node scripts/load-check.mjs
```

통과 조건:

- 표 2의 단일 배포 latency/error gate를 모두 충족.
- application CPU 평균 70% 이하, 5분 평균 85% 초과 없음.
- memory가 warm-up 종료 후 20분 동안 10% 이상 지속 증가하지 않음.
- DB connection pool wait p99 100ms 이하, pool exhaustion 0회.
- Cockroach transaction retry로 최종 실패한 요청 0.1% 이하.
- custom ingest의 accepted+duplicate+rejected 합계가 입력 event 수와 정확히 일치.
- 시험 전후 fact 및 audit invariant 검사 차이 0건.

결과 JSON, k6 summary, dashboard snapshot, 이미지 SHA를 staging 증거 디렉터리에 저장하거나 만료되지 않는 CI artifact URL로 연결합니다.

## 7. 장애 시험

각 시험은 10분 steady state, 10분 장애, 10분 회복으로 수행합니다.

| 장애 | 주입 | 통과 조건 |
| --- | --- | --- |
| provider `429` | fixture proxy가 `Retry-After`와 함께 응답 | jitter/backoff 적용, 다른 provider SLO 유지, retry storm 없음 |
| provider `5xx`/timeout | 30초 timeout과 connection reset 혼합 | 실패 격리, stale 정책 준수, queue age 30분 미만 |
| API instance 종료 | 부하 중 instance 1개 SIGTERM | 신규 오류 0.1% 이하, drain 내 종료, 중복 job 0건 |
| sync worker 종료 | fact 저장 전·후 각각 강제 종료 | lease 만료 뒤 재개, idempotency 유지, 고아 running job 0건 |
| Cockroach node 장애 | staging cluster node 1개 중단 | readiness가 안전하게 반응하고 RTO 15분 이내, 데이터 불변식 유지 |
| DB 연결 단절 | application에서 DB endpoint 5분 차단 | mutation은 안정된 `503`, secret/log 유출 없음, 복구 후 pool 정상화 |
| backup storage 장애 | external connection 접근 거부 | production 요청 영향 없음, backup page 발생, 다음 성공까지 RPO 추적 |

파괴적 장애 주입은 production에서 수행하지 않습니다. CockroachDB node 시험은 production과 같은 복제 구성을 가진 격리 staging에서만 수행합니다.

## 8. 보안 시험 gate

PR 필수 gate:

```sh
go test ./apps/api/... -race
govulncheck ./apps/api/...
gosec ./apps/api/...
yarn npm audit --all --recursive
gitleaks detect --no-git --redact
```

주간 또는 release gate:

- 인증된/비인증 route를 구분한 DAST. destructive endpoint는 격리 tenant만 사용.
- SSRF fixture: loopback, link-local, RFC1918, metadata endpoint, DNS rebinding, redirect 후 재검증.
- replay/만료/소유권 우회, oversized payload, slow body, rate-limit 회피 시험.
- container image와 OS package 취약점 scan, SBOM 보관.

통과 기준은 신규 critical/high 0건입니다. 예외는 owner, 영향, 완화, 만료일(최대 30일)을 가진 risk acceptance가 있어야 하며 staging evidence에 링크합니다.

## 9. 측정 기록

각 실행은 `docs/evidence/staging/README.md` 형식으로 다음을 남깁니다.

- UTC 시작/종료, 실행자, commit SHA, API/Web image digest.
- CockroachDB exact version과 migration checksum 목록.
- 부하 profile과 raw result artifact.
- dashboard/alert/trace 링크.
- 각 SLO의 실측치와 pass/fail.
- 장애·보안 시험별 관찰, recovery time, 열린 이슈.
- 결론과 승인자. 실행하지 않은 행은 `NOT RUN`으로 둡니다.
