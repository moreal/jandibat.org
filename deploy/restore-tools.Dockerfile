FROM cockroachdb/cockroach:v26.2.5@sha256:771325a0586bf61d53322d24f5a6de8962568b0fc181fa45db364278e5961282

# api/maintenance are immutable OCI layout build contexts from Nix archives.
# Keep the referenced Nix store closure because archive entrypoints are symlinks.
COPY --from=api /nix/store /nix/store
COPY --from=maintenance /nix/store /nix/store
COPY --from=api /bin/server /jandibat-api
COPY --from=maintenance /bin/maintenance /jandibat-maintenance
COPY --from=api /busybox /busybox

WORKDIR /workspace
