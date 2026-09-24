# 정적 GraphQL 소비자 가이드

도메인 조회 계약은 [`graphql/schema/`](../graphql/schema/)의 SDL입니다. 정적 사이트·배치·CI는 Relay 없이 `POST /graphql`에 이름 있는 GraphQL query 하나를 보내 `ActivitySnapshot`을 가져올 수 있습니다. 초기 API에는 subscription이 없으므로 변경 알림을 기다리지 말고 cron이나 CI schedule로 주기적으로 다시 조회·빌드하세요.

## ActivitySnapshot 조회

`subject(handleOrID)`는 공개 handle 또는 로컬 subject ID를 받습니다. 범위의 양 끝 날짜를 포함하며 timezone은 IANA 이름입니다. `environmentIDs`는 생략하거나 `null`이면 접근 가능한 전체 environment를 포함하고, 배열이면 해당 ID만 필터링합니다. 익명 소비자는 public subject의 공개 projection만 받습니다. 인증된 owner에게 보이는 private 데이터가 섞인 결과를 공개 사이트에 게시하지 마세요.

```sh
curl --fail-with-body --silent --show-error \
  --request POST 'https://api.example.com/graphql' \
  --header 'content-type: application/json' \
  --data-binary '{"operationName":"StaticActivitySnapshot","query":"query StaticActivitySnapshot($subject: String!, $range: DateRangeInput!, $timezone: TimeZone!, $environmentIDs: [String!]) { subject(handleOrID: $subject) { activitySnapshot(range: $range, timezone: $timezone, environmentIDs: $environmentIDs) { subject { handle displayName } range { from to } days { date count level } total longestStreak generatedAt dataUpdatedAt revision } } }","variables":{"subject":"alice","range":{"from":"2026-01-01","to":"2026-01-31"},"timezone":"Asia/Seoul","environmentIDs":["github"]}}'
```

HTTP 성공만 확인하지 말고 응답 JSON의 `errors`가 비어 있고 `data.subject.activitySnapshot`이 있는지도 확인해야 합니다. GraphQL 요청 실패는 HTTP 200과 함께 오류 객체로 전달될 수 있습니다.

정적 응답의 `total`과 day `count`는 JavaScript 정밀도를 보존하기 위해 십진 문자열인 GraphQL `Long`입니다. 숫자 변환이 필요한 소비자는 안전 범위를 확인하거나 `BigInt`를 사용하세요.

## 실행 가능한 TypeScript 예제

[`examples/graphql-static-renderer/`](../examples/graphql-static-renderer/)는 Node 24에서 추가 패키지 설치 없이 실행됩니다. 단일 GraphQL POST를 수행하고 HTML과 같은 이름의 JSON sidecar를 생성합니다.

```sh
nix develop --command sh -c 'cd examples/graphql-static-renderer && npm test'
nix develop --command node --experimental-strip-types examples/graphql-static-renderer/src/index.ts \
  --endpoint 'https://api.example.com/graphql' \
  --subject alice \
  --from 2026-01-01 \
  --to 2026-01-31 \
  --timezone Asia/Seoul \
  --environment-id github \
  --output ./activity.html
```

`--environment-id`는 여러 번 줄 수 있고 생략할 수 있습니다. 이 예제는 익명 공개 조회용이며 인증 credential을 받거나 저장하지 않습니다. `activity.html`에는 `generatedAt`이 보이고 `dataUpdatedAt`이 있으면 함께 보입니다. `activity.html.json`에는 `generatedAt`, nullable `dataUpdatedAt`, `revision` 세 provenance 필드와 렌더링된 제목 비교용 `renderedSubjectLabel`이 기록됩니다. GraphQL/HTTP 오류가 나면 파일을 교체하지 않습니다.

## 갱신과 provenance

- `generatedAt`: 서버가 이 snapshot을 생성한 시각(UTC). 동일한 데이터라도 매 조회마다 달라질 수 있습니다.
- `dataUpdatedAt`: 조회 범위에 데이터 변경이 마지막으로 반영된 시각. 반영된 변경이 한 번도 없으면 필드는 응답에 남고 값은 `null`입니다.
- `revision`: 조회 범위·인증 가시성·활동 내용을 반영한 안정적인 식별자. 같은 revision과 같은 표시 제목이면 예제는 HTML과 sidecar의 교체를 건너뛰어 정적 배포 churn을 줄입니다. 제목만 바뀌면 새 HTML을 씁니다. 건너뛴 경우 CLI 출력에는 이번 조회의 최신 `generatedAt`이 나오지만, 파일에는 마지막으로 게시된 snapshot의 provenance가 그대로 남습니다.

예를 들어 매시간 cron이나 CI schedule에서 예제를 실행하고, 파일이 변경된 경우에만 정적 사이트를 다시 배포할 수 있습니다. `generatedAt`만 달라져도 `revision`이 같을 수 있으며 이는 실패가 아닙니다. 다른 날짜 범위·timezone·environment 필터 또는 공개/owner 가시성은 별도 artifact로 관리하세요. `revision` 비교는 같은 조회 조건의 이전 결과에만 적용해야 합니다.

## SVG 임베드와의 차이

GraphQL `ActivitySnapshot`은 정적 소비자가 자체 HTML·이미지·JSON을 빌드하는 도메인 값 객체이며 Relay Node가 아닙니다. 기존 Markdown 이미지나 `<img>` 태그에 즉시 넣을 SVG가 필요하면 별도 HTTP edge인 [`GET /v1/render/{subject}.svg`](EMBED_GUIDE.ko.md)를 사용하세요. OpenAPI는 이 SVG render, health, 인증 callback/consume, custom activity ingest 같은 HTTP edge만 설명합니다. 과거 REST domain 경로를 정적 소비자 API로 사용하지 마세요.
