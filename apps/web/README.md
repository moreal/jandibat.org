# `@jandibat/web`

This package contains the accessible Solid 2 TypeScript SPA for jandibat.org.
It uses the official Solid Vite plugin in client-only `start` mode, filesystem
routing, and domain-scoped Solid components. It includes the public one-year
heatmap, authentication, provider connections, custom activity ingestion, and
embed-code generator. The architecture decision and complexity controls are
recorded in [`../../docs/FRONTEND_ARCHITECTURE_DECISION.ko.md`](../../docs/FRONTEND_ARCHITECTURE_DECISION.ko.md).

## Local development

The API defaults to `http://localhost:8080` in development. Override it when
needed:

```bash
VITE_API_BASE_URL=http://localhost:8080 yarn workspace @jandibat/web dev
```

Production builds use the same origin by default. The container can point an
already-built SPA at another API origin at startup, without rebuilding assets:

```bash
docker build -f apps/web/Dockerfile -t jandibat-web .
docker run --rm -p 8081:8080 \
  -e JANDIBAT_API_BASE_URL=https://api.example.com \
  jandibat-web
curl --fail http://localhost:8081/healthz
```

The build emits the static client application under `dist/client`. The
container runs as UID/GID `101`, serves `/healthz` directly, and falls back to
the generated `index.html` for client-side routes. Its startup hook validates
`JANDIBAT_API_BASE_URL` as HTTPS, JSON-escapes it into non-executable
`/config.json`, and serves that file with `no-store`. The browser validates the
URL again before loading the application. Production responses include a CSP
that permits scripts and styles only from the same origin. At startup the same
validated URL is reduced to its origin and written to a non-root-owned Nginx
include under `/tmp`, so `connect-src` contains only `'self'` plus that exact
origin (or only `'self'` when the API URL is empty). API path prefixes never
enter the CSP header. `img-src` is likewise limited to `'self'`, `data:`, and
the same exact API origin, so arbitrary HTTPS image hosts are not allowed.
`VITE_API_BASE_URL` remains available for non-container build-time deployments,
while `JANDIBAT_API_BASE_URL` takes precedence at runtime.

## API contracts

GraphQL SDL under `../../graphql/schema/` defines the domain API. Route
operations and fragments generate types and artifacts under
`src/pages/__generated__/`; the Solid 2 UI reads domain server state through
the project Relay adapter in `src/relay/` and its normalized store. The
`solid-relay` workspace fork is pinned to an exact upstream revision; see
`../../packages/solid-relay/UPSTREAM.md`. Local view models from
`@jandibat/contracts` (for example, heatmap display data and owner-subject
presentation) are projections of GraphQL results, not a second wire contract.

OpenAPI at `../../openapi/jandibat.yaml` describes only HTTP edge endpoints:
health, SVG rendering, magic-link consumption, provider OAuth callback, and
custom activity ingestion. `src/api/client.ts` uses its generated types only
for the two browser-initiated HTTP edge calls. Embed SVG URLs are built
separately; domain queries and mutations must not be added to that REST client.

After changing either contract, run generation and drift checks inside the
Nix development shell:

```bash
nix develop -c make graphql-generate openapi-types
nix develop -c make graphql-check openapi-check typecheck-web
```

For a static, non-Relay GraphQL consumer, see
[`../../docs/GRAPHQL_STATIC_CONSUMER_GUIDE.ko.md`](../../docs/GRAPHQL_STATIC_CONSUMER_GUIDE.ko.md).

## Verification

```bash
yarn workspace @jandibat/web test
yarn workspace @jandibat/web typecheck
yarn workspace @jandibat/web build
```
