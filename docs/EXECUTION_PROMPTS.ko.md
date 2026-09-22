# 구현 실행 프롬프트

아래 프롬프트는 새 Codex 세션에서 그대로 사용할 수 있다. 한 세션에서 모든 현대화를 동시에
수행하지 않고 계획별로 별도 branch/worktree를 사용한다. 각 계획을 완료·검토·병합한 뒤 다음
계획으로 이동한다.

## 1. Nix·버전·Go 품질·Zap

```text
이 저장소의 AGENTS.md와 docs/PLATFORM_MODERNIZATION_DESIGN.ko.md를 읽고,
docs/superpowers/plans/2026-09-22-nix-go-quality-modernization.md를 구현하세요.

superpowers:using-git-worktrees로 격리된 worktree를 만들고,
superpowers:subagent-driven-development 방식으로 계획의 Task를 순서대로 수행하세요.
각 Task는 테스트 우선으로 구현하고 독립적으로 검토·커밋하세요. Solid는 반드시 2.x를
유지하고, Nix를 유일한 toolchain 버전 원천으로 만드세요. 기존 사용자 변경을 덮어쓰지 마세요.
완료 전 superpowers:verification-before-completion과
superpowers:requesting-code-review를 적용하세요.
```

## 2. CockroachDB baseline·Scythe

M0/M1 계획이 병합된 뒤 실행한다.

```text
이 저장소의 AGENTS.md, docs/PLATFORM_MODERNIZATION_DESIGN.ko.md,
docs/QUERY_BUILDER_DECISION.ko.md를 읽고,
docs/superpowers/plans/2026-09-22-cockroach-scythe-baseline.md를 구현하세요.

superpowers:using-git-worktrees와 superpowers:subagent-driven-development를 사용하세요.
기존 CockroachDB 데이터와 migration history는 폐기해도 되지만, 로컬/운영 PVC를 실제로
삭제하기 전에는 정확한 대상을 확인하고 사용자 승인을 받으세요. 먼저 Nix에 고정된 공식 원본
Scythe로 CockroachDB + Go pgx probe를 수행하고, 재현되는 결함이 있을 때만 계획의 Task 7
패치를 작성하세요. 각 adapter group을 별도 테스트·검토·커밋하고 최종적으로 fresh database,
live Scythe verification, race test를 모두 실행하세요.
```

## 3. GraphQL·Relay·정적 소비자 API

M2 계획이 병합된 뒤 실행한다.

```text
이 저장소의 AGENTS.md, docs/PLATFORM_MODERNIZATION_DESIGN.ko.md,
docs/MODERNIZATION_BACKLOG.ko.md를 읽고,
docs/superpowers/plans/2026-09-22-graphql-relay-static-api.md를 구현하세요.

superpowers:using-git-worktrees와 superpowers:subagent-driven-development를 사용하세요.
GraphQL SDL을 domain API 계약으로, OpenAPI를 HTTP edge 계약으로 유지하세요. 공식 프론트엔드는
Solid 2 + relay-runtime + solid-relay를 사용하되 third-party API를 로컬 adapter 뒤에 두세요.
초기 GraphQL subscription은 구현하지 마세요. ActivitySnapshot의 generatedAt,
dataUpdatedAt, revision 의미와 anonymous/owner visibility를 계약 테스트로 먼저 고정하세요.
REST endpoint는 대응 GraphQL acceptance test와 프론트엔드 이전이 끝난 뒤에만 제거하세요.
완료 전 schema/codegen drift, 보안 회귀, 타입 검사, production build를 검증하세요.
```

## 4. 전체 통합과 배포 handoff

앞의 세 계획과 homelab 계획이 각각 검토된 뒤 실행한다.

```text
jandibat.org와 ../homelab의 AGENTS.md, 양쪽 jandibat 설계/구현 계획을 모두 읽으세요.
두 저장소에서 아직 완료되지 않은 M4/H1/H2 항목만 식별하고, 저장소별 별도 worktree와 커밋으로
통합 검증을 수행하세요. jandibat.org는 immutable image와 runtime contract까지만 소유하고,
Kubernetes/Flux/SOPS/backup provider 설정은 homelab이 소유해야 합니다. 실제 cluster reconcile,
PVC/database 폐기, secret 편집, DNS 적용은 preview/plan과 정확한 대상을 보여준 뒤 사용자 승인을
받으세요. 최종 완료 조건은 docs/PLATFORM_MODERNIZATION_DESIGN.ko.md §10입니다.
```
