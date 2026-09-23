# jandibat.org Agent Working Agreement

이 저장소는 백엔드/프론트엔드를 병렬로 구현하는 것을 기본 전제로 합니다.

## 공통 원칙

- 계약 우선(Contract-first): GraphQL SDL(`graphql/schema/**`)이 도메인 API 계약이고,
  `openapi/jandibat.yaml`은 HTTP edge endpoint 계약입니다.
- 백엔드/프론트엔드는 각 계약을 기준으로 독립 구현합니다. 기존 REST 도메인 경로는
  GraphQL·Relay 화면 전환이 끝날 때까지 임시로 유지하고 이후 OpenAPI에서 제거합니다.
- API 변경은 해당 GraphQL SDL 또는 OpenAPI 변경과 변경 로그를 같은 PR에 포함합니다.
- 도메인 로직은 순수 함수로 유지하고, 부수 효과(I/O, 네트워크, DB)는 애플리케이션/어댑터 계층으로 분리합니다.

## 소유권 경계

- Backend Agent: `apps/api/**`
- Frontend Agent: `apps/web/**`, `packages/contracts/**`
- Platform/Coordination Agent: 루트 설정, `docs/**`, `openapi/**`, CI

## 충돌 방지

- 루트 파일(`package.json`, `Makefile`, CI)은 Coordination Agent만 수정합니다.
- 서로의 소유 경계 파일은 건드리지 않습니다.
- 인터페이스 요구사항은 `docs/interface-change-log.md`에 기록합니다.

## 병렬 구현 체크리스트

- 도메인 필드는 GraphQL SDL에, HTTP edge 엔드포인트는 OpenAPI에 정의
- 백엔드: 핸들러 및 응답 형식 구현
- 프론트엔드: Relay/edge 타입 재생성 및 UI/요청 코드 반영
- Contract test 또는 snapshot으로 계약 준수 확인
