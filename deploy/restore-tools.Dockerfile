ARG API_IMAGE
FROM ${API_IMAGE} AS application

FROM cockroachdb/cockroach:v26.2.5@sha256:771325a0586bf61d53322d24f5a6de8962568b0fc181fa45db364278e5961282

COPY --from=application /jandibat-api /jandibat-api
COPY --from=application /jandibat-maintenance /jandibat-maintenance
COPY --from=application /busybox /busybox

WORKDIR /workspace
