# 프론트엔드 아키텍처 결정: Solid 2 공식 start mode

> 상태: **부분 채택 / 나머지는 역사적 결정 기록**. Solid 2, 공식 Vite `start: true`
> client-only mode, 런타임 `/config.json` 부트스트랩만 유지합니다. 아래 본문의 정확한
> `rc.0`·`next.28` 버전, OpenAPI 단일 DTO/API 경계, `AppStateProvider`·지역 signal 중심의
> 서버 상태 및 데이터 패칭 라이브러리 미사용 규칙은 더 이상 현행 지침이 아닙니다.
> 도메인 API는 GraphQL SDL, 공식 UI 서버 상태는 고정 revision의 `solid-relay`/Relay
> normalized store가 담당합니다. 현재 계약과 버전·진행 상태는
> [`PLATFORM_MODERNIZATION_DESIGN.ko.md`](PLATFORM_MODERNIZATION_DESIGN.ko.md),
> [`MODERNIZATION_BACKLOG.ko.md`](MODERNIZATION_BACKLOG.ko.md),
> [`../graphql/schema/`](../graphql/schema/)를 참조하세요. 아래의 `결정`·`개발 복잡성 통제`·
> `호환성과 결과`는 2026-08-14 당시의 배경 설명으로 보존합니다.

- 상태: 부분 채택(현행 범위는 위 안내 참조)
- 결정일: 2026-08-14
- 범위: `apps/web/**`

## 배경

기존 프론트엔드는 프레임워크 없는 TypeScript SPA였다. 단일 `main.ts`가 해시 라우팅,
화면 마크업 생성, DOM 이벤트 연결, 요청 상태와 전역 상태를 함께 관리했다. 초기 구현은
단순했지만 인증·Provider·커스텀 데이터·임베드 기능이 늘면서 화면 갱신 단위와 상태 수명이
불명확해지고, 문자열 템플릿과 수동 DOM 바인딩을 검증하는 비용이 커졌다.

Solid 2 RC 시점에는 기존 SolidStart가 Solid 1 기반으로 유지되며 Solid 2를 지원하지 않는다.
Solid 팀은 SolidStart의 후속 경로로 공식 Vite 플러그인의 `start: true` 모드를 제공한다.
따라서 Solid 2와 기존 SolidStart 패키지를 혼합하지 않고, 공식 start mode를 사용한다.

- [Solid 2.0.0 RC 릴리스](https://github.com/solidjs/solid/releases/tag/v2.0.0-rc.0)
- [SolidStart에서 start mode로 이전](https://v2.solidjs.com/migration/from-solid-start)

## 결정

`apps/web`을 다음 구성의 client-only TypeScript SPA로 운영한다.

- `solid-js@2.0.0-rc.0`, `@solidjs/web@2.0.0-rc.0`
- `@solidjs/vite-plugin@3.0.0-next.28`의 `solid({ start: true })`
- `filesystem-routing`과 `@solidjs/router` 기반 파일 라우팅
- Nginx 정적 호스팅과 browser-history fallback
- 런타임 `/config.json`을 읽은 뒤 애플리케이션을 마운트하는 기존 배포 계약 유지

SSR은 도입하지 않는다. 이 애플리케이션의 인증 상태와 데이터는 브라우저에서 API로
불러오며, 현재 컨테이너는 빌드 후에도 API origin을 바꿀 수 있어야 한다. start mode의
client-only 출력(`dist/client`)은 이 운영 모델을 유지하면서 공식 라우팅과 코드 분할을
제공한다.

## 개발 복잡성 통제

복잡성은 프레임워크 자체가 아니라 경계와 검증 규칙으로 제한한다.

1. 라우트는 `src/routes/**`에서 URL과 화면만 연결하고, 실제 화면은 `src/pages/**`에 둔다.
2. 공유 브라우저 상태는 `AppStateProvider` 하나로 제한한다. 서버 데이터는 각 라우트의
   지역 signal에서 관리하고 전역 캐시 계층을 추가하지 않는다.
3. API 경로와 DTO 경계는 계속 `src/api/client.ts`와 OpenAPI 생성 타입에만 둔다.
4. 달력 계산, 동의 판정, 임베드 코드 생성은 DOM과 분리된 순수 함수로 유지한다.
5. 컴포넌트는 `innerHTML` 기반 렌더링을 사용하지 않는다. 외부 문자열 경계는 Solid의
   텍스트/속성 바인딩과 컴포넌트 보안 테스트로 검증한다.
6. 라우트 단위 lazy chunk를 유지하고 별도 상태관리·데이터 패칭 라이브러리는 실제
   중복 요구가 생기기 전까지 추가하지 않는다.
7. RC/next 의존성은 정확한 버전으로 고정하고, 타입검사·순수 함수 테스트·DOM 컴포넌트
   테스트·프로덕션 빌드·컨테이너 smoke를 승격 조건으로 사용한다.

## 호환성과 결과

- 기존 `#explore`, `#connections`, `#auth`, `#custom`, `#embed` 링크는 시작 시 browser
  route로 변환한다.
- 기존 백엔드의 정확한 OAuth/Magic Link redirect allowlist를 바꾸지 않기 위해 callback
  발급 주소는 해시 형식을 유지하고, 도착 즉시 토큰과 일회성 파라미터를 제거한다.
- API 및 OpenAPI 계약은 변경하지 않는다.
- 과거 `index.html` + `bootstrap.ts` + `main.ts` 진입점과 수동 DOM view는 제거한다.

## 재검토 조건

Solid 2 및 start mode 패키지가 stable로 전환되면 정확한 버전 고정을 갱신한다. 서버 렌더링이
실제 제품 요구가 되거나 Nginx 런타임 설정 모델이 바뀌는 경우에는 SSR 도입을 별도 결정으로
검토한다.
