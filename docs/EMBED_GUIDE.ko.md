# SVG 임베드 가이드

기준 계약: `openapi/jandibat.yaml` 1.1.0
대상 endpoint: `GET /v1/render/{subject}.svg`

이 endpoint는 Markdown이나 HTML의 이미지 URL로 사용할 수 있는 self-contained SVG를 반환합니다. 아래의 `API_BASE_URL`은 실제 배포의 `PUBLIC_BASE_URL`로 바꿉니다. 저장소에는 운영 hostname이 고정되어 있지 않으므로 예시에서는 `https://api.example.com`을 사용합니다.

## URL 만들기

기본 형식은 다음과 같습니다.

```text
${API_BASE_URL}/v1/render/${encodeURIComponent(subject)}.svg?query
```

`subject`는 subject ID 또는 유일한 public handle입니다. 길이는 1~64자이고 첫 글자는 영문자 또는 숫자, 나머지는 영문자·숫자·`.`·`_`·`-`만 허용됩니다. query 값은 URL encoding하고, `environmentId`는 값을 여러 번 포함해 필터링합니다.

```ts
const apiBaseUrl = "https://api.example.com";
const subject = "alice";
const url = new URL(
  `/v1/render/${encodeURIComponent(subject)}.svg`,
  apiBaseUrl,
);

url.searchParams.set("from", "2026-01-01");
url.searchParams.set("to", "2026-08-12");
url.searchParams.set("timezone", "Asia/Seoul");
url.searchParams.append("environmentId", "github");
url.searchParams.append("environmentId", "custom:work-log");
url.searchParams.set("theme", "github-dark");
url.searchParams.set("cellSize", "11");
url.searchParams.set("showLegend", "true");
url.searchParams.set("weekStart", "monday");

console.log(url.toString());
```

## Render parameter

| parameter | 허용값과 제한 | 생략 시 동작 |
| --- | --- | --- |
| `from` | `YYYY-MM-DD`, 포함 범위 시작일 | `to`의 364일 전 |
| `to` | `YYYY-MM-DD`, 포함 범위 종료일 | 선택 timezone의 오늘 |
| `timezone` | IANA timezone, 1~64자. 예: `Asia/Seoul` | OpenAPI 계약은 subject 설정값. 현재 서버 조립의 fallback은 `UTC`이므로 재현 가능한 임베드는 명시 권장 |
| `force` | `true` 또는 `false` | `false`. `true`는 인증된 subject owner만 사용할 수 있음 |
| `environmentId` | 포함할 environment ID, 각 1~128자. 최대 20개까지 parameter를 반복 | 모든 environment |
| `theme` | `system`, `light`, `dark`, `github-light`, `github-dark` | `system`. 현재 renderer는 `system`·`light`·`github-light`를 light palette로, 나머지 두 값을 dark palette로 렌더링 |
| `cellSize` | 정수 `6`~`32`, CSS pixel | `11` |
| `showLegend` | `true` 또는 `false` | `true` |
| `weekStart` | `sunday` 또는 `monday` | `sunday` |

`from`과 `to`는 양 끝을 모두 포함하며 `from <= to`여야 합니다. 기본 조회 범위는 365일이고 현재 timeline 서비스는 윤년 범위를 포함해 최대 366일을 허용합니다. 동일한 `environmentId`가 반복되면 현재 handler는 한 번만 적용합니다.

공개 activity/SVG GET은 항상 non-destructive `keep_stale` 정책을 사용하며 `failurePolicy` query를 받지 않습니다. `purge`는 subject owner가 설정한 정책을 인증된 sync mutation과 background scheduler가 적용할 때만 사용합니다. `force=true`는 cache freshness를 우회하고 provider 호출을 유발할 수 있으며 인증과 ownership 검사가 필요합니다. 공개 Markdown 이미지 URL에 넣지 마십시오.

## Markdown 예시

```markdown
![alice의 활동 히트맵](https://api.example.com/v1/render/alice.svg?from=2026-01-01&to=2026-08-12&timezone=Asia%2FSeoul&theme=github-dark&cellSize=11&showLegend=true&weekStart=monday&environmentId=github&environmentId=custom%3Awork-log)
```

URL이 길어지면 Markdown reference link를 사용할 수 있습니다.

```markdown
![alice의 활동 히트맵][jandibat-heatmap]

[jandibat-heatmap]: https://api.example.com/v1/render/alice.svg?timezone=Asia%2FSeoul&theme=github-light&weekStart=monday
```

## HTML 예시

HTML attribute 안에서는 query separator `&`를 `&amp;`로 씁니다.

```html
<img
  src="https://api.example.com/v1/render/alice.svg?timezone=Asia%2FSeoul&amp;theme=github-dark&amp;cellSize=12&amp;showLegend=true&amp;weekStart=monday"
  alt="alice의 최근 활동 히트맵"
  loading="lazy"
  decoding="async"
>
```

테마별 이미지를 명시적으로 전환하려면 `<picture>`를 사용할 수 있습니다. 현재 `theme=system`은 SVG 내부에서 client color scheme을 동적으로 선택하지 않고 light palette로 정규화되므로, 자동 전환이 필요하면 두 URL을 구분합니다.

```html
<picture>
  <source
    media="(prefers-color-scheme: dark)"
    srcset="https://api.example.com/v1/render/alice.svg?theme=github-dark"
  >
  <img
    src="https://api.example.com/v1/render/alice.svg?theme=github-light"
    alt="alice의 최근 활동 히트맵"
  >
</picture>
```

## Cache와 갱신

- 인증 정보 없이 읽은 public subject의 성공 응답은 현재 `Cache-Control: public, max-age=300`이며 5분 동안 공유 cache에 저장될 수 있습니다. 이 응답에는 global environment와 해당 subject 소유이며 `visibility=private`로 표시되지 않은 subject-scoped environment가 포함됩니다.
- Public subject라도 owner 인증으로 `visibility=private`인 connection fact까지 포함한 응답과 private subject의 owner 응답은 `Cache-Control: private, no-store`입니다. 인증된 non-owner가 public subject를 읽으면 anonymous view와 같은 공개 범위와 cache policy가 적용됩니다.
- 성공 응답에는 SVG bytes의 SHA-256 기반 `ETag`가 포함됩니다. 현재 handler는 `If-None-Match`에 대한 `304 Not Modified` 처리를 구현하지 않으므로, ETag만 보고 conditional request가 지원된다고 가정하지 않습니다.
- `force=true`는 HTTP cache header를 바꾸는 옵션이 아니라 내부 timeline freshness를 우회하는 owner 전용 옵션입니다. 중간 CDN이나 Markdown renderer가 이미 저장한 이미지를 즉시 무효화하지는 않습니다.
- URL query를 바꾸면 별도 cache key가 됩니다. 불필요한 cache-busting query를 계속 추가하면 provider/API 부하가 증가할 수 있습니다.

## Privacy와 외부 renderer

공개 문서에는 public subject의 anonymous view만 임베드합니다. 이 view에는 global environment뿐 아니라 해당 subject의 custom provider activity도 포함될 수 있습니다. Credential-backed private provider connection처럼 `visibility=private`인 subject-scoped environment는 owner에게만 보이며, `environmentId` query로 지정해도 anonymous 접근 범위를 넓힐 수 없습니다. Custom activity 공개를 원하지 않으면 해당 subject 자체를 private으로 전환해야 합니다.

GitHub 같은 Markdown renderer, image proxy, 브라우저 확장, CDN은 이미지 URL과 query를 관찰하고 자체 cache나 로그에 남길 수 있습니다. 따라서 URL에 session token, API key, email 또는 다른 secret을 넣지 않습니다.

private subject는 유효한 session cookie 또는 bearer token을 가진 owner만 조회할 수 있습니다. 일반적인 Markdown image proxy는 이 인증 정보를 전달하지 않으며, `jandibat_session` cookie는 `Secure`, `HttpOnly`, `SameSite=Lax`입니다. private SVG를 공개 임베드로 우회하려고 credential을 URL에 넣어서는 안 됩니다.

`environmentId`, 날짜 범위, timezone도 공개 URL의 일부입니다. 이 값 자체가 조직명·업무 패턴 같은 정보를 드러낼 수 있으면 임베드 범위를 줄이거나 공개하지 않습니다.
