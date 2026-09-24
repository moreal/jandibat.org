# 멀티에이전트 병렬 구현 가이드

> 초기 OpenAPI 단일 계약 시절의 병렬 작업 가이드입니다. 아래 REST 도메인 티켓과 단일
> OpenAPI 규칙은 현대화 작업에 적용하지 않습니다. 현재 도메인 계약은
> [`graphql/schema/`](../graphql/schema/)의 SDL, HTTP edge 계약은
> [`openapi/jandibat.yaml`](../openapi/jandibat.yaml)입니다. 현행 작업 분할과 완료 기준은
> [`MODERNIZATION_BACKLOG.ko.md`](MODERNIZATION_BACKLOG.ko.md)와 저장소 `AGENTS.md`를
> 따르세요.

## 목적

백엔드(Go/chi)와 프론트엔드(Yarn)를 동시에 개발하면서 충돌을 줄이고 속도를 높입니다.

## 권장 에이전트 구성

1. Coordination Agent
- 소유 파일: 루트 설정, `openapi/**`, `docs/**`, CI
- 역할: API 계약 관리, 작업 분할, 병합 검증

2. Backend Agent
- 소유 파일: `apps/api/**`
- 역할: OpenAPI 기준 핸들러/서비스 구현

3. Frontend Agent
- 소유 파일: `apps/web/**`, `packages/contracts/**`
- 역할: OpenAPI 타입 기반 UI/API 클라이언트 구현

## 병렬 개발 규칙

- OpenAPI를 단일 소통 매개체로 사용
- API 변경 시 백엔드/프론트엔드 동시 변경 강제하지 않음
- 프론트엔드는 OpenAPI 기반 타입 생성으로 선행 개발 가능
- 백엔드는 스펙 기반 mock/stub 응답으로 선행 개발 가능

## 작업 흐름

1. Coordination Agent가 OpenAPI 변경 반영
2. Backend Agent가 서버 구현/테스트
3. Frontend Agent가 타입 생성 및 화면/임베드 UX 구현
4. Coordination Agent가 계약 호환성 검증 후 병합

## 작업 티켓 예시

- B-101: `GET /v1/activities/{subject}` 실제 provider aggregation 구현
- B-102: `GET /v1/render/{subject}.svg` SVG 렌더러 구현
- F-101: Subject 페이지(최근 1년 heatmap)
- F-102: Markdown 임베드 가이드/샘플 생성기
- C-101: OpenAPI lint + generated types CI 추가

## Done 기준

- OpenAPI와 실제 응답 구조 일치
- 백엔드 테스트 통과
- 프론트엔드 타입체크 통과
- 문서(임베드/연동/인증) 최신화

## 실행 기준 문서

- 구현 순서도 + 캐시 시퀀스 + ERD: `docs/IMPLEMENTATION_BLUEPRINT.ko.md`
