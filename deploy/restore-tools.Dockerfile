FROM scratch AS jandibat-binaries
COPY jandibat-api /jandibat-api
COPY jandibat-maintenance /jandibat-maintenance
COPY busybox /busybox
COPY bin/backup-tools /workspace/bin/backup-tools

FROM cockroachdb/cockroach:v26.2.5@sha256:771325a0586bf61d53322d24f5a6de8962568b0fc181fa45db364278e5961282 AS cockroach-donor

FROM cgr.dev/chainguard/glibc-dynamic:latest@sha256:6acf5a19a988abdaf0f3d30247561431a206034e702871442bed66a2c68cc1a2

# Keep the runtime's APK database, OS libraries, and CA trust untouched.
COPY --from=cockroach-donor /cockroach/cockroach /cockroach/cockroach
COPY --from=cockroach-donor /licenses/LICENSE /licenses/LICENSE
COPY --from=cockroach-donor /licenses/THIRD-PARTY-NOTICES.txt /licenses/THIRD-PARTY-NOTICES.txt

# These regular files were dereferenced from the exact imported Nix image IDs.
COPY --from=jandibat-binaries /jandibat-api /jandibat-api
COPY --from=jandibat-binaries /jandibat-maintenance /jandibat-maintenance
COPY --from=jandibat-binaries /busybox /busybox
COPY --from=jandibat-binaries /workspace/bin/backup-tools /workspace/bin/backup-tools
COPY applets/ /usr/bin/
COPY db/migrations/ /workspace/db/migrations/
COPY scripts/ /workspace/scripts/

ENV PATH="/cockroach:/bin:/usr/bin" SSL_CERT_FILE="/etc/ssl/certs/ca-certificates.crt"
WORKDIR /workspace
USER 65532:65532
ENTRYPOINT ["/cockroach/cockroach"]
CMD ["version"]
