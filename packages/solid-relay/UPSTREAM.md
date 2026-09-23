# Upstream source provenance

- Project: [XiNiHa/solid-relay](https://github.com/XiNiHa/solid-relay)
- Upstream release: `1.0.0-beta.29`
- Exact Git commit: `9dfb839e4f0e11abf2a09af1702954570f11d609`
- Upstream `src` Git tree: `13be8ed95f9e7e32e930860b43e5c1068bf95c3c`
- Pristine `src` SHA-256 manifest digest: `6aedcb9284e5141621c36b29b11da2bec9572caafa230386b8270b4cf9ca542a`
- Pristine `LICENSE` SHA-256: `01f75b42c3d06b233fb7bfa209bceccb21d04dba28943de35b8dbb8d18b88663`

At the pristine-import commit, the 23 files under `src/` and `LICENSE` were copied verbatim from that upstream commit. `package.json` is local workspace metadata and is not an upstream file. The digest above identifies that immutable base snapshot; subsequent Solid 2 compatibility patches intentionally change local source files.

## Local Solid 2 port

The local package export surface is deliberately limited to the environment provider, lazy query, fragment, and mutation primitives proven by the Task 6 compatibility test. Upstream's pagination, refetch, preload, query-loader, and subscription exports are omitted until each needed path has a Solid 2 and Relay 21 runtime regression test; initial subscriptions are out of scope. The official frontend is a statically served client, so this port does not support server rendering or the upstream `deferStream` behavior. The pinned upstream source remains available in this package for auditable patch review. Any changes after the pristine import are local compatibility patches, not upstream files.

To reproduce the `src` manifest digest from an upstream checkout at the recorded commit:

```sh
git ls-files src | xargs shasum -a 256 | shasum -a 256
```
