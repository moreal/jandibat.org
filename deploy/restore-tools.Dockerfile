FROM scratch AS jandibat-binaries
COPY jandibat-api /jandibat-api
COPY jandibat-maintenance /jandibat-maintenance
COPY busybox /busybox
COPY bin/backup-tools /workspace/bin/backup-tools

FROM cockroachdb/cockroach:v26.2.5@sha256:771325a0586bf61d53322d24f5a6de8962568b0fc181fa45db364278e5961282

# These regular files were dereferenced from the exact imported Nix image IDs.
COPY --from=jandibat-binaries /jandibat-api /jandibat-api
COPY --from=jandibat-binaries /jandibat-maintenance /jandibat-maintenance
COPY --from=jandibat-binaries /busybox /busybox
COPY --from=jandibat-binaries /workspace/bin/backup-tools /workspace/bin/backup-tools
COPY db/migrations/ /workspace/db/migrations/
COPY scripts/ /workspace/scripts/

WORKDIR /workspace
USER 65532:65532
