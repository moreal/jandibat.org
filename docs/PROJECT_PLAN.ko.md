# jandibat.org 프로젝트 계획

> 이 문서는 제품 범위와 단계 정의를 설명합니다. 항목별 완료 상태와 재현 가능한 검증 명령은 [`DELIVERY_CHECKLIST.ko.md`](DELIVERY_CHECKLIST.ko.md)를 단일 진행 현황으로 사용합니다.

## 1. 프로젝트 목표

jandibat.org는 GitHub Activity Heatmap(잔디밭) 개념을 확장한 AGPL 오픈소스 플랫폼입니다.

핵심 목표:

- GitHub, GitLab, Codeberg 및 확장 가능한 Custom Provider에서 Activity 데이터 수집
- 정규화된 Activity API 제공
- SVG 기반 서버 사이드 렌더링 제공(README/블로그 Markdown 임베드 용도)
- 사용자 인증: Passkey + Email Magic Link
- 인증 후 사용자별 커스텀 데이터 provider 등록 및 외부 서비스 연동

## 2. 기술 방향

- 모노레포: 단일 저장소 (`apps/api`, `apps/web`, `packages/contracts`)
- 프론트엔드 패키지 매니저: Yarn (`.yarnrc.yml`에서 `nodeLinker: pnpm`)
- 프론트엔드: Solid 2 + 공식 Vite `start` mode 기반 client-only TypeScript SPA
- 백엔드: Go + chi
- 인터페이스 계약: OpenAPI (`openapi/jandibat.yaml`)
- 데이터베이스: CockroachDB 26.2 계열 + `pgx`의 `database/sql` adapter

## 3. 아키텍처 개요

1. Ingestion Layer
- 각 provider(GitHub/GitLab/Codeberg/Custom)에서 활동 데이터를 가져옴
- 원천 데이터를 공통 Activity 이벤트 스키마로 정규화

2. Core API Layer
- 정규화 Activity 조회 API
- Provider 연결/동기화 트리거 API
- 인증 및 사용자 설정 API

3. Rendering Layer
- Heatmap SVG SSR 렌더링
- 테마/기간/색상 등 파라미터화

4. Frontend Layer
- Solid filesystem route 기반 사용자 인증, provider 연결, 커스텀 데이터 소스 관리 UI
- 상세 결정과 복잡성 통제 기준은 [`FRONTEND_ARCHITECTURE_DECISION.ko.md`](FRONTEND_ARCHITECTURE_DECISION.ko.md)를 따름

## 4. 데이터 모델(초기)

- Subject: heatmap 대상 식별자(사용자/프로젝트/커스텀 엔터티)
- ActivityDay: `date`, `count`, `level(0..4)`, `sources[]`
- ProviderConnection: provider 인증 상태, 동기화 메타데이터
- CustomProvider: 사용자 정의 입력 소스(예: 독서 기록)

## 5. 단계별 실행 계획

Phase 0: 기반 구성
- 모노레포 골격 구축
- OpenAPI 초안 수립
- 백엔드/프론트엔드 병렬 개발 경계 정의

Phase 1: MVP (Public Activity)
- GitHub public activity 수집
- Activity 조회 API + SVG 렌더링
- 임베드 문서화

Phase 2: 인증/연동
- Passkey + Magic Link 인증
- 외부 provider 인증(OAuth/token)
- Private activity 수집

Phase 3: 확장성
- Custom provider SDK/API
- GitLab/Codeberg 정식 통합
- 동기화 스케줄러 및 운영성 강화

## 6. 리스크 및 대응

- Provider API 변경 리스크: adapter 계층 분리 + 계약 테스트
- 시간대/집계 기준 불일치: UTC 저장 + 렌더링 시 타임존 적용
- 인증 복잡도: Passkey/Magic Link를 auth 모듈로 분리
- CockroachDB 종속성 확산: storage interface와 adapter 경계를 유지하고 migration checksum을 검증
