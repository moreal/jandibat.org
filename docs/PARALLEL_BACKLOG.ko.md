# 병렬 구현 백로그 (초기)

기준일: 2026-03-02

> 2026-03 초기 구현의 역사적 백로그입니다. 체크 상태와 REST 경로는 당시의 기록으로
> 유지하며 현행 완료 판정이나 신규 작업 계약으로 사용하지 않습니다. 현재 범위·진행 상태는
> [`MODERNIZATION_BACKLOG.ko.md`](MODERNIZATION_BACKLOG.ko.md), API 계약은
> [`graphql/schema/`](../graphql/schema/)의 SDL과
> [`openapi/jandibat.yaml`](../openapi/jandibat.yaml)의 HTTP edge만 참조하세요.

## 상태 표기

- `[x]`: 저장소에 구현과 검증 경로가 모두 존재함
- `[ ]`: 미완료 또는 통합 검증 전

전체 단계의 완료 판정은 [`DELIVERY_CHECKLIST.ko.md`](DELIVERY_CHECKLIST.ko.md)를 따릅니다.

## Coordination Agent (계약/운영)

- [x] C-001: OpenAPI lint 파이프라인 구성
- [x] C-002: OpenAPI 변경 시 `apps/web` 타입 생성 CI 구성
- [x] C-003: `docs/interface-change-log.md` 자동 업데이트 규칙 정의
- [x] C-004: 인증/연동 보안 체크리스트 작성

## Backend Agent (Go + chi)

- [x] B-001: `GET /v1/activities/{subject}` provider 집계 서비스 계층 구현
- [x] B-002: `GET /v1/render/{subject}.svg` SVG 렌더러 구현
- [x] B-003: `POST /v1/auth/magic-link/request` 실제 메일 송신 연동
- [x] B-004: Passkey 등록/검증(WebAuthn) 엔드포인트 구현
- [x] B-005: provider 연결(OAuth/token) 상태 저장 모델 구현
- [x] B-006: storage interface 설계 + Cockroach 구현체 추가

## Frontend Agent (Yarn)

- [x] F-001: OpenAPI generated type 기반 API client 래퍼 작성
- [x] F-002: Subject heatmap 뷰(1년 뷰 + 툴팁) 구현
- [x] F-003: Provider 연결 관리 페이지 구현
- [x] F-004: Magic Link/Passkey 인증 화면 구현
- [x] F-005: README/블로그 임베드 URL 생성기 UI 구현

## 병렬 의존성 규칙

- F 계열 작업은 OpenAPI 스펙만 확정되면 B 완료 전에도 진행 가능
- B 계열 신규 응답 필드 추가 시 C를 통해 OpenAPI 선반영 후 구현
- B/F 공통 차단 이슈는 `interface-change-log.md`를 단일 진실 원천으로 사용
