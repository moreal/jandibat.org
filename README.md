# jandibat.org

AGPL 기반의 오픈소스 Activity Heatmap 플랫폼입니다.

## 목표

- GitHub, GitLab, Codeberg 및 커스텀 Provider에서 Activity 데이터 수집
- API 제공 + SSR 렌더링(SVG/이미지) 제공
- Passkey 및 Email Magic Link 인증
- 개인 커스텀 Activity provider 등록

## 저장소 구조

- `apps/api`: Go(chi) 기반 API/SSR 서버
- `apps/web`: Solid 2 start mode 기반 TypeScript SPA (Yarn, `nodeLinker: pnpm`)
- `packages/contracts`: 활동 표시 모델의 순수 변환 도우미 패키지
- `graphql/schema`: 조회·변경 도메인 API의 기준 SDL
- `openapi`: health/SVG/callback/custom ingest HTTP edge 계약
- `docs`: 기획/의사결정/협업 문서

## 현재 상태

현재 현대화 범위와 단계별 검증 상태는 [`docs/MODERNIZATION_BACKLOG.ko.md`](docs/MODERNIZATION_BACKLOG.ko.md)를 기준으로 관리합니다. 이전 Phase 0~3의 완료 기록과 설계 초안은 [`docs/DELIVERY_CHECKLIST.ko.md`](docs/DELIVERY_CHECKLIST.ko.md), [`docs/PROJECT_PLAN.ko.md`](docs/PROJECT_PLAN.ko.md), [`docs/PARALLEL_BACKLOG.ko.md`](docs/PARALLEL_BACKLOG.ko.md)에 역사적 자료로 남겨 두었습니다.

운영 배포·backup/restore의 완료는 코드 존재만으로 주장하지 않습니다. 실제 환경의 검증
증거와 미완료 항목은 현대화 백로그 및 [`docs/evidence/staging/README.md`](docs/evidence/staging/README.md)에 기록합니다.

## 로컬 요구 사항

- Nix(flakes 활성화): Go, Node.js, Yarn, Scythe 및 Go 분석 도구 버전의 단일 진실 원천은 `flake.nix`입니다.
- Docker Compose(CockroachDB를 사용하는 통합 개발 시)

환경 변수의 기본값, 형식, production 필수값은 [`docs/CONFIGURATION.ko.md`](docs/CONFIGURATION.ko.md)에 정리돼 있습니다. 로컬 시작점은 추적 가능한 `.env.example`이며 실제 `.env`와 모든 `.env.*` 파일은 커밋하지 않습니다.

## 빠른 시작

Nix를 사용하면 저장소에 고정된 Go, Node.js, Yarn 및 분석 도구를 한 번에 사용할 수 있습니다.

```sh
nix develop
yarn install --immutable
make ci
```

첫 `yarn install --immutable`은 네트워크에서 패키지를 받아 추적하지 않는 로컬 `.yarn/cache`를
채웁니다. 그 뒤에는 `yarn install --immutable --immutable-cache`로 네트워크 없는 재설치를
검증할 수 있습니다. `make nix-check`는 모든 선언 시스템의 flake를 평가한 뒤 현재 호스트의
고정 Yarn offline cache와 toolchain interface check를 빌드하는 저장소 무결성 게이트입니다.

셸에 들어가지 않고 전체 검사를 실행하려면 `nix develop --command make ci`를 사용합니다.
direnv 사용자는 선택적으로 `direnv allow`를 실행하면 추적된 `.envrc`가 같은 flake 개발 셸을
자동으로 활성화합니다.

Nix를 사용하지 않는 경우에는 다음과 같이 로컬 도구를 준비합니다.

```sh
mise install # 선택 사항: 저장소에 고정된 Go/Node 버전 설치
corepack enable
yarn install --immutable
cp .env.example .env
make check
```

개별 개발 서버와 데이터베이스는 다음 명령으로 실행합니다.

- 프론트엔드: `make dev-web`
- API: `make dev-api`
- Sync worker: `make dev-worker`
- Retention/re-encryption: `make dev-maintenance`
- CockroachDB: `make db-up && make db-migrate`

`make check`는 GraphQL SDL/gqlgen/Relay artifact와 OpenAPI edge 타입의 drift 검사,
secret/shell 검사, Go·프론트엔드 테스트, 프론트엔드 타입검사와 프로덕션 빌드를 실행합니다.
Nix flake 검사와 의존성 설치까지 포함해 CI와 같은 검사를 고정된 도구 버전으로 재현하려면
`nix develop --command make ci`를 사용합니다.

도메인 GraphQL 계약을 변경했다면 SDL, gqlgen/Relay generated artifact, 변경 로그를 한
변경 단위로 다루고 `make graphql-check`로 drift를 확인합니다. HTTP edge 계약을 변경했다면
다음 세 파일을 한 변경 단위로 다룹니다.

1. `openapi/jandibat.yaml`
2. `apps/web/src/generated/api.ts` (`make openapi-types`로 재생성하는 HTTP edge 타입)
3. `docs/interface-change-log.md`

PR 전에는 `make ci`가 통과하는지 확인합니다. 인증, provider 연결 또는 외부 입력을 다루는 변경은 [`docs/SECURITY_CHECKLIST.ko.md`](docs/SECURITY_CHECKLIST.ko.md)도 함께 검토합니다.

## 계약 우선 병렬 개발

- 도메인 API 계약: `graphql/schema/**/*.graphqls` (`POST /graphql`)
- HTTP edge 계약: `openapi/jandibat.yaml`
- 변경 로그 및 생성물 drift: `docs/interface-change-log.md`, `make graphql-check openapi-check`
- 에이전트 작업 가이드: `AGENTS.md`
- 초기 병렬 실행 가이드(역사적): `docs/MULTI_AGENT_PARALLEL_GUIDE.ko.md`
- 초기 구현 순서도/ERD(역사적): `docs/IMPLEMENTATION_BLUEPRINT.ko.md`
- ERD(개념 + Cockroach 물리 초안): `docs/ERD_CONCEPTUAL_AND_PHYSICAL.ko.md`
- 로컬 Cockroach 실행 가이드: `docs/LOCAL_DEV_COCKROACH.ko.md`
- 초기 프로젝트 계획 문서(역사적): `docs/PROJECT_PLAN.ko.md`
- 프론트엔드 아키텍처 결정: `docs/FRONTEND_ARCHITECTURE_DECISION.ko.md`
- 초기 Phase 0~3 완료/검증 기록(역사적): `docs/DELIVERY_CHECKLIST.ko.md`
- 런타임 환경 변수와 secret 형식: `docs/CONFIGURATION.ko.md`
- 인증·연동 보안 체크리스트: `docs/SECURITY_CHECKLIST.ko.md`
- Go 쿼리빌더 의사결정 초안: `docs/QUERY_BUILDER_DECISION.ko.md`
- 도메인 저장/조회 규약: `docs/DOMAIN_PERSISTENCE_MODEL.ko.md`
- SVG Markdown/HTML 임베드: `docs/EMBED_GUIDE.ko.md`
- Custom provider SDK/API: `docs/CUSTOM_PROVIDER_SDK.ko.md`
- SLO와 운영 검증 기준: `docs/SLO.ko.md`
- 배포·rollback·migration: `docs/runbooks/DEPLOY_ROLLBACK_MIGRATION.ko.md`
- Runtime DB 역할·최소권한: `docs/runbooks/DATABASE_ROLES.ko.md`
- backup·restore: `docs/runbooks/BACKUP_RESTORE.ko.md`
- staging 증거 상태: `docs/evidence/staging/README.md`
