# GraphQL Relay and Static API Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Replace browser-facing REST domain operations with a Relay-compatible GraphQL API and migrate the Solid 2 frontend to a normalized Relay store while publishing a provenance-rich static ActivitySnapshot API.

**Architecture:** GraphQL SDL is the domain contract and gqlgen maps resolvers to existing application ports. Durable entities implement Relay Node identity; activity snapshots remain range-valued objects. HTTP/OpenAPI remains only for health, rendering, callbacks, and ingestion. The Solid-specific Relay integration is isolated behind a project adapter.

**Tech Stack:** GraphQL SDL, gqlgen, Relay compiler/runtime, solid-relay, Solid 2, TypeScript

**Spec:** `docs/PLATFORM_MODERNIZATION_DESIGN.ko.md`

## Global Constraints

- GraphQL subscriptions are not part of the initial implementation.
- External consumers are static query clients and must not need Relay.
- `generatedAt` is server time in UTC RFC 3339; `dataUpdatedAt` is the last incorporated data-change time; `revision` is stable for equivalent snapshots.
- Only Subject, ProviderConnection, CustomProvider, SyncJob, and Session implement Node.
- Cursor connections are for unbounded collections; bounded catalogs and activity days remain lists.
- Health, SVG render, OAuth/magic-link callback, and custom ingest remain HTTP/OpenAPI operations.
- Breaking changes are allowed, but schema drift and authorization regressions are not.
- Task 1 establishes GraphQL SDL authority while existing REST domain operations remain transitional; Task 8 enforces the final edge-only OpenAPI path list after the Relay UI migration. This resolves the otherwise contradictory Task 1/Task 8 ordering without a red plan-end contract gate.

## Review Focus

- A global ID for one type must never decode as another type even when database IDs match; Task 2 tests cross-type substitution.
- Anonymous, owner, and authenticated non-owner snapshot visibility must produce distinct authorized data and revisions; Task 3 covers all three.
- `generatedAt` may change while `revision` remains stable; Task 3 pins this cache semantic.
- Relay pagination must neither duplicate nor skip rows with equal timestamps; Task 4 uses `(sort_time, id)` cursors.
- GraphQL aliases/fragments must not bypass complexity, rate-limit, or field authorization policy; Task 5 adds adversarial operation tests.

---

### Task 1: Establish the split contract and gqlgen pipeline

**Files:**
- Create: `graphql/schema/scalars.graphqls`
- Create: `graphql/schema/node.graphqls`
- Create: `graphql/schema/query.graphqls`
- Create: `graphql/schema/mutation.graphqls`
- Create: `apps/api/gqlgen.yml`
- Create: `apps/api/internal/graphql/generated/`
- Create: `apps/api/internal/graphql/model/`
- Create: `scripts/check-graphql-generated.sh`
- Modify: `Makefile`
- Modify: `AGENTS.md`
- Modify: `docs/interface-change-log.md`
- Modify: `package.json`

**Interfaces:**
- Consumes: application services/ports and the contract-first working agreement
- Produces: `make graphql-generate`, `graphql-check`, and gqlgen resolver interfaces

- [ ] **Step 1: Add a contract test that requires two authorities**

Write a repository-level test that asserts GraphQL SDL owns domain query/mutation types and OpenAPI contains only the approved HTTP edge paths. Initially assert at least the SDL files/generation command exist so it fails before scaffolding.

- [ ] **Step 2: Run the contract test against the REST-only repository**

Run: `nix develop --command make graphql-check`

Expected: FAIL because GraphQL schema/generated artifacts do not exist.

- [ ] **Step 3: Add minimal schema and gqlgen generation**

Define `Date`, `DateTime`, `TimeZone`, `Cursor`, `Node`, a placeholder `Query`, and mutation error/result conventions. Configure generated execution code under `internal/graphql/generated`, resolver code under `internal/graphql`, and explicit model bindings for domain-owned types.

- [ ] **Step 4: Add deterministic server and client generation checks**

Run gqlgen and Relay/compiler TypeScript generation, then fail if a second generation changes files. `graphql-check` must validate SDL, compile generated Go, and diff generated artifacts.

- [ ] **Step 5: Update the working agreement and commit**

Change AGENTS from “OpenAPI is the single API contract” to “GraphQL SDL owns domain API; OpenAPI owns HTTP edge API.” Preserve ownership boundaries and require interface change log entries for both.

### Task 2: Implement Relay object identity

**Files:**
- Create: `apps/api/internal/graphql/relayid/codec.go`
- Create: `apps/api/internal/graphql/relayid/codec_test.go`
- Modify: `graphql/schema/node.graphqls`
- Create: `apps/api/internal/graphql/node_resolver.go`
- Create: `apps/api/internal/graphql/node_resolver_test.go`

**Interfaces:**
- Consumes: entity-specific IDs from application ports
- Produces: `relayid.Encode(kind Kind, raw string) string`; `relayid.Decode(global string) (Kind, string, error)`; root `node(id: ID!): Node`

- [ ] **Step 1: Write opaque-ID contract tests**

Test round-trip for every allowed Node kind, malformed base64/token input, unknown kind, empty raw ID, oversized input, and cross-type substitution. Assert error messages never echo a secret-bearing raw token.

- [ ] **Step 2: Run tests before implementation**

Run: `nix develop --command sh -c 'cd apps/api && go test ./internal/graphql/relayid ./internal/graphql -run Node'`

Expected: FAIL because codec and resolver do not exist.

- [ ] **Step 3: Implement a versioned opaque codec**

Encode a version, type discriminator, and raw ID using URL-safe base64 without treating base64 as encryption. Reject unrecognized versions and enforce a bounded decoded length. Keep parsing in one package.

- [ ] **Step 4: Resolve Nodes with authorization preserved**

Dispatch by decoded kind to application ports. Apply the same owner/public visibility rules as direct root fields. Return `null` for a deleted or inaccessible node rather than leaking existence through distinguishable errors.

- [ ] **Step 5: Generate, test, and commit**

Run GraphQL generation, codec/resolver tests, and the existing security test suite. Commit schema, codec, and Node resolver together.

### Task 3: Implement the static ActivitySnapshot query

**Files:**
- Create: `graphql/schema/activity.graphqls`
- Create: `apps/api/internal/graphql/activity_resolver.go`
- Create: `apps/api/internal/graphql/activity_resolver_test.go`
- Modify: `apps/api/internal/application/activity/get_timeline.go`
- Modify: activity models/ports as required for provenance
- Create: `apps/api/internal/application/activity/revision.go`
- Create: `apps/api/internal/application/activity/revision_test.go`

**Interfaces:**
- Consumes: subject lookup, authorized activity facts, environment visibility, range and timezone
- Produces: `subject(handleOrID).activitySnapshot(range, timezone, environmentIDs)` with provenance fields

- [ ] **Step 1: Write snapshot semantics tests**

Cover inclusive ranges, leap day, invalid timezone, maximum range, duplicate environment IDs, anonymous/public data, owner/private data, authenticated non-owner data, empty activity, and provider refresh failure. Assert `generatedAt` falls within request start/end server times.

- [ ] **Step 2: Write deterministic revision tests**

Assert equivalent ordered facts and authorization scope yield the same revision despite a later `generatedAt`; changed count/date/environment/visibility/dataUpdatedAt yields a different revision; input row order does not affect it.

- [ ] **Step 3: Run tests and observe missing provenance behavior**

Run: `nix develop --command sh -c 'cd apps/api && go test ./internal/application/activity ./internal/graphql -run "Snapshot|Revision"'`

Expected: FAIL because `generatedAt`, `dataUpdatedAt`, and `revision` are not implemented as specified.

- [ ] **Step 4: Implement provenance at the application boundary**

Capture one server time after the consistent read, derive `dataUpdatedAt` from incorporation/update metadata rather than the activity's historical date, and hash a canonical serialization of query scope and authorized result for `revision`.

- [ ] **Step 5: Implement and generate the GraphQL resolver**

Map GraphQL inputs to the existing pure timeline logic and map domain errors to stable GraphQL error extensions without exposing internals.

- [ ] **Step 6: Test authorization/cache semantics and commit**

Run activity, GraphQL, HTTP cache/security regression, and race tests. Commit schema, application provenance, resolver, and tests together.

### Task 4: Add Relay connections and domain mutations

**Files:**
- Create: `graphql/schema/auth.graphqls`
- Create: `graphql/schema/subjects.graphqls`
- Create: `graphql/schema/integrations.graphqls`
- Create: `apps/api/internal/graphql/cursor/codec.go`
- Create: `apps/api/internal/graphql/cursor/codec_test.go`
- Create/modify: resolvers under `apps/api/internal/graphql/`
- Modify: application ports that currently expose offset/REST pagination

**Interfaces:**
- Consumes: authenticated viewer and existing auth/subject/integration services
- Produces: viewer, subjects, provider connections, custom providers, sessions and sync jobs GraphQL operations; Session/SyncJob connections

- [ ] **Step 1: Add cursor pagination tests**

Use more than one row with the same timestamp and assert forward traversal has no duplicates/gaps. Test malformed cursor, deleted cursor anchor, limit zero/negative/over max, empty page, and stable ordering by `(timestamp, id)`.

- [ ] **Step 2: Implement versioned opaque keyset cursors**

Decode only the expected connection kind and sort tuple. Cap `first` at the schema-defined maximum. Do not expose database offsets as cursors.

- [ ] **Step 3: Define queries/mutations with typed payloads**

Model validation/domain failures as typed payload fields and reserve GraphQL errors for transport/auth/internal failures. Return affected Node objects from mutations so Relay can normalize them without custom store updates where possible.

- [ ] **Step 4: Implement resolvers by domain group**

Complete and commit independently in this order: viewer/auth, subjects/settings, provider connections/sync jobs, custom providers. Run focused unit/contract/security tests for each group.

- [ ] **Step 5: Run generated drift and full resolver tests**

Run `make graphql-generate graphql-check test-api-race`. Expected: no generated diff and all new operations honor application ports rather than accessing DB adapters directly.

### Task 5: Harden the GraphQL HTTP boundary

**Files:**
- Create: `apps/api/internal/graphql/server.go`
- Create: `apps/api/internal/graphql/server_test.go`
- Modify: `apps/api/internal/http/router.go`
- Modify: HTTP auth, request ID, rate-limit, metrics, and audit middleware as required
- Modify: `apps/api/internal/http/security_regression_test.go`

**Interfaces:**
- Consumes: gqlgen executable schema and existing session/bearer authentication
- Produces: `POST /graphql`; development-only introspection/playground policy; operation-aware logs/metrics

- [ ] **Step 1: Write adversarial operation tests**

Cover GET rejection, wrong content type, oversized bodies, batched requests, aliases that repeat expensive fields, deep fragments, cyclic fragments, anonymous private access, CSRF/cookie mutation attempts, malformed variables, and internal error redaction.

- [ ] **Step 2: Mount GraphQL with existing cross-cutting controls**

Reuse request IDs, authentication, rate limits, recovery, audit transaction semantics, and metrics. Add query depth/complexity limits and a production introspection policy. Do not add WebSocket transport.

- [ ] **Step 3: Test operation naming and observability**

Ensure Zap logs and metrics record operation name/type but never variables or full query text that may contain user data. Require named operations outside explicitly allowed development mode.

- [ ] **Step 4: Run HTTP and security suites**

Run GraphQL server tests plus all current router/security/audit/rate-limit tests. Expected: PASS with no regression in HTTP edge endpoints.

- [ ] **Step 5: Commit the GraphQL transport**

Keep transport middleware changes separate from domain resolver commits.

### Task 6: Validate and isolate solid-relay

**Files:**
- Create: `apps/web/src/relay/environment.ts`
- Create: `apps/web/src/relay/index.ts`
- Create: `apps/web/src/relay/compat.ts`
- Create: `apps/web/test/relay-compat.test.tsx`
- Create: `relay.config.json`
- Modify: `apps/web/package.json`
- Modify: `package.json`
- Modify: `yarn.lock`

**Interfaces:**
- Consumes: GraphQL schema, pinned `relay-runtime`, pinned `solid-relay` revision
- Produces: project-owned `RelayProvider`, query/fragment/mutation primitives, generated TypeScript artifacts

- [ ] **Step 1: Write a Solid 2 compatibility test application**

Mount a provider, execute a mocked query, render a fragment, commit a mutation response containing the same Node ID, and assert both consumers update from one normalized record. Dispose the root and assert store/network subscriptions are released.

- [ ] **Step 2: Pin dependencies and run the spike**

Pin exact Relay compiler/runtime versions and an exact solid-relay Git revision. Run the compatibility test under Solid 2. Expected: either PASS or a concrete adapter incompatibility; do not proceed on an unexplained failure.

- [ ] **Step 3: Hide third-party APIs behind the local adapter**

Re-export only the primitives the app uses. If a compatibility patch is necessary, keep it as a small workspace package or Nix/Yarn patch with a regression test and upstream link; do not scatter workarounds through pages.

- [ ] **Step 4: Configure Relay compiler drift checks**

Generate TypeScript artifacts from component fragments/operations and fail CI when generation changes tracked files.

- [ ] **Step 5: Commit the proven integration boundary**

Commit dependency pins, adapter, compiler configuration, and compatibility tests before migrating pages.

### Task 7: Migrate the Solid frontend by route

**Files:**
- Modify: `apps/web/src/App.tsx`
- Modify: `apps/web/src/app/state.tsx`
- Modify: pages/components under `apps/web/src/pages/**` and `apps/web/src/**`
- Delete after migration: domain portions of `apps/web/src/api/client.ts`
- Delete after migration: `apps/web/src/generated/api.ts` domain usages
- Modify: frontend tests under `apps/web/test/**`

**Interfaces:**
- Consumes: Task 6 adapter and generated Relay artifacts
- Produces: official UI whose remote server state is owned by Relay; local UI/session bootstrap state remains Solid-native

- [ ] **Step 1: Add a cross-route normalization acceptance test**

Render two components reached through different routes/fragments that reference the same Subject ID. Apply one mutation response and assert both views update without duplicate manual writes or full-page refetch.

- [ ] **Step 2: Install the Relay provider at the application boundary**

Keep runtime `/config.json` loading before environment creation. Authentication credentials/cookies remain in the network function; never store tokens in Relay records.

- [ ] **Step 3: Migrate routes in independently testable groups**

Migrate and commit in this order: explore/activity, connections, custom providers, auth/session, embed builder. For each route colocate fragments with consuming components, use generated types, and remove the corresponding REST client method.

- [ ] **Step 4: Reduce `AppStateProvider` to true client state**

Remove Subject/provider/session server caches and request state. Retain only UI concerns that are not server records, such as transient navigation or local preferences not persisted by the API.

- [ ] **Step 5: Verify no legacy domain client remains**

Run `rg` for old REST paths, generated OpenAPI domain types, and manual server-state setters. Every remaining match must correspond to an approved HTTP edge endpoint.

- [ ] **Step 6: Run frontend verification and commit**

Run GraphQL generation/check, typecheck, Vitest/Node tests, production build, and web container smoke. Commit each route group separately and the cleanup last.

### Task 8: Shrink OpenAPI and publish the static-consumer guide

**Files:**
- Modify: `openapi/jandibat.yaml`
- Modify: `packages/contracts/**`
- Modify: `docs/EMBED_GUIDE.ko.md`
- Create: `docs/GRAPHQL_STATIC_CONSUMER_GUIDE.ko.md`
- Create: `examples/graphql-static-renderer/package.json`
- Create: `examples/graphql-static-renderer/src/index.ts`
- Create: `examples/graphql-static-renderer/test/static-renderer.test.ts`
- Modify: `docs/interface-change-log.md`
- Modify: contract tests under `apps/api/internal/http/`

**Interfaces:**
- Consumes: implemented GraphQL schema and approved HTTP edge list
- Produces: minimal OpenAPI contract and runnable static consumer examples that display/preserve provenance

- [ ] **Step 1: Write contract assertions for the remaining HTTP paths**

Assert OpenAPI contains health, SVG render, callbacks/consume, and custom ingest only. Assert removed domain paths are absent and GraphQL schema exposes their replacements.

- [ ] **Step 2: Remove REST domain routes and schemas**

Delete handlers only after their GraphQL acceptance tests and frontend migrations pass. Preserve shared application services and edge HTTP behavior.

- [ ] **Step 3: Write a tested static query example**

The example accepts endpoint, subject, date range, timezone, and optional environment IDs; executes one GraphQL query; renders a deterministic artifact; visibly prints `generatedAt`; prints `dataUpdatedAt` when present; and embeds all three provenance values in metadata/sidecar output.

- [ ] **Step 4: Document refresh and revision behavior**

State that consumers are static, should rebuild via cron/CI, may skip artifact replacement when `revision` is unchanged, and must not expect subscription. Explain that `generatedAt` can advance without a revision change.

- [ ] **Step 5: Run final contract and application verification**

Run:

```bash
nix develop --command make graphql-check openapi-check contract-change-check
nix develop --command make test-api test-api-race test-web typecheck build-web
```

Expected: all pass; generation produces no diff; only approved REST calls remain in the frontend.

- [ ] **Step 6: Commit the protocol cutover**

Commit OpenAPI shrink, route removal, change log, guide, and runnable example together so the breaking change is reviewable as one cutover.
