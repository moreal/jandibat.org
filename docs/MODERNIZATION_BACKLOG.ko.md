# 플랫폼 현대화 백로그

- 기준일: 2026-09-22
- 설계: [`PLATFORM_MODERNIZATION_DESIGN.ko.md`](PLATFORM_MODERNIZATION_DESIGN.ko.md)
- 상태 표기: `[ ]` 미착수, `[-]` 진행 중, `[x]` 검증 완료

각 항목은 구현만 끝났다고 완료하지 않는다. 명시된 계약·테스트·운영 evidence가 같은 변경에
있어야 `[x]`로 바꾼다.

## M0. 재현 가능한 기반

- [x] M0-01: 루트 Nix flake와 lockfile을 추가하고 Go 1.27.1, Node 24.21.0 LTS,
  Yarn 4.18.0 및 프로젝트 도구를 고정한다.
- [x] M0-02: Yarn Berry offline dependency derivation을 `fetchYarnBerryDeps`와
  `yarnBerryConfigHook`으로 구성한다.
- [x] M0-03: Makefile 명령을 Nix shell 안의 안정적인 인터페이스로 정리한다.
- [x] M0-04: CI의 setup-go/setup-node/corepack pin을 Nix 기반 실행으로 교체한다.
- [x] M0-05: TypeScript 7.0.2, Vite 8.3.0, Vitest 5.0.1 및 Solid 2 RC package를 동일
  release 계열로 올리고 전체 build/test를 복구한다.

## M1. Go 품질과 관측성

- [x] M1-01: staticcheck, exhaustive, go-check-sumtype을 Nix와 Makefile에 고정한다.
- [x] M1-02: 기존 enum switch를 exhaustive하게 만들고 sum-type marker 규칙을 적용한다.
- [x] M1-03: 표준 `log` wrapper를 주입형 Zap logger로 교체하고 redaction 회귀 테스트를 둔다.
- [x] M1-04: server/worker/maintenance의 JSON log schema와 종료 시 flush를 검증한다.

## M2. CockroachDB baseline과 typed SQL

- [x] M2-01: 기존 migration을 폐기하고 현재 schema의 단일 baseline migration을 작성한다.
- [x] M2-02: 공식 원본 Scythe의 CockroachDB + Go pgx probe를 실제 DB에서 실행한다.
- [x] M2-03: `pgxpool`/`pgx.Tx` executor와 SQLSTATE 40001 retry boundary를 확정한다.
- [x] M2-04: 정적 adapter query를 Scythe SQL block과 generated Go 코드로 전환한다.
- [x] M2-05: 동적 운영 query는 typed parameter/variant를 우선하고 남은 identifier 조합을
  allowlist test로 고정한다.
- [x] M2-06: `scythe generate`, offline drift check, live Cockroach verification을 CI에 추가한다.
- [x] M2-07: 공식 Scythe 결함이 입증될 때만 Nix patch와 최소 재현 fixture를 추가한다.

M2 구현·격리 DB 검증은 완료했다. Task 7의 upstream issue/PR 제출은 외부 저장소 쓰기
승인 대기 중이며, 위 M2-07 체크는 로컬 patch와 최소 재현 검증만 뜻한다. 초기
`0001_baseline.sql` 이후 예약 fencing을 위한 additive `0002`가 적용된다.

## M3. GraphQL·Relay 계약

- [x] M3-01: GraphQL SDL을 추가하고 gqlgen generation/drift check를 구성한다.
- [x] M3-02: Relay global ID codec, `Node` query와 durable entity identity test를 구현한다.
- [x] M3-03: ActivitySnapshot query와 `generatedAt`, `dataUpdatedAt`, `revision` 의미를 구현한다.
- [x] M3-04: sessions/sync jobs에 cursor connection을 적용하고 bounded list는 배열로 유지한다.
- [ ] M3-05: 기존 REST domain endpoint를 GraphQL query/mutation으로 옮긴다.
- [ ] M3-06: OpenAPI를 health/SVG/callback/ingest endpoint로 축소하고 contract change log를
  갱신한다.
- [ ] M3-07: `solid-relay` compatibility spike를 통과시키고 고정 revision과 어댑터 경계를
  확정한다.
- [ ] M3-08: 공식 Solid 2 UI를 generated Relay artifact와 normalized store로 전환한다.
- [ ] M3-09: 수동 `AppStateProvider` 서버 상태와 OpenAPI-generated domain DTO를 제거한다.
- [ ] M3-10: 정적 GraphQL 소비자 활용 가이드와 TypeScript/curl 예제를 추가한다.

M3-01~04는 생성물 drift, API/race·격리 Cockroach 통합 테스트, 스키마·권한 검사를
통과했다. Subject와 provider connection·custom provider도 owner-scoped keyset connection을
사용하며, 내장 provider catalog 3종만 bounded 배열이다. GraphQL HTTP 경계와 공식 UI
전환은 아직 남아 있어 외부 서비스가 새 query를
호출할 수 있는 단계로 간주하지 않는다. `dataUpdatedAt`은 조회 범위에 반영된 변경이
한 번도 없을 때만 `null`이며, 기존 DB의 migration history와 데이터는 이전하지 않는다.

## M4. 이미지와 런타임 계약

- [ ] M4-01: Nix/고정 toolchain으로 API, worker, maintenance, web image를 재현 가능하게 빌드한다.
- [ ] M4-02: runtime configuration, health/readiness, migration Job 입력과 최소 권한 DB role을
  문서화하고 container smoke test로 고정한다.
- [ ] M4-03: immutable tag/digest와 SBOM/vulnerability scan을 유지한다.

## H1. homelab Talos 배포 (`../homelab`)

- [ ] H1-01: `services/jandibat/k8s`와 전용 Flux Kustomization을 추가한다.
- [ ] H1-02: official CockroachDB Helm chart secure single-node와 persistent volume을 배포한다.
- [ ] H1-03: SOPS secret, migration Job, API/worker/maintenance/web workload를 연결한다.
- [ ] H1-04: NetworkPolicy로 DB 접근을 jandibat workload와 maintenance job에 제한한다.
- [ ] H1-05: ingress route와 DNS를 플랫폼 inventory를 통해 선언한다.
- [ ] H1-06: image digest automation과 rollback 절차를 검증한다.

## H2. off-site backup과 복구 (`../homelab`)

- [ ] H2-01: provider-neutral S3 secret schema와 external connection bootstrap Job을 추가한다.
- [ ] H2-02: Cockroach native hourly incremental/daily full/35-day schedule을 idempotent하게 만든다.
- [ ] H2-03: backup freshness/failure metric scrape와 alert를 추가한다.
- [ ] H2-04: bucket versioning/immutability를 선택 provider에서 검증한다.
- [ ] H2-05: 격리 restore Job/runbook과 주간 smoke·월간 full drill evidence template을 추가한다.
- [ ] H2-06: 실제 restore drill에서 RPO 1시간, RTO 4시간을 입증한다.

## 의존 순서

```text
M0 ──► M1
 │
 ├──► M2 ──► M3 ──► M4 ──► H1 ──► H2
 │             │
 └─────────────┘
```

M1은 M2/M3와 파일 소유권이 겹칠 수 있으므로 composition root와 observability 변경은 먼저
완료한다. H1은 배포할 immutable image와 runtime contract가 준비된 M4 이후 적용한다. H2의
manifest 작성은 H1과 함께 검토할 수 있지만 실제 schedule과 restore evidence는 CockroachDB가
기동된 뒤에만 완료한다.
