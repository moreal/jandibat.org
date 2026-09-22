# syntax=docker/dockerfile:1.7

FROM --platform=$BUILDPLATFORM golang:1.27.1-alpine3.24@sha256:8a5910f31396cd4d89662f56c68b3ae31d374308270a1c3bd96672ee5ed43414 AS build

WORKDIR /src

COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod \
    go mod download

COPY . .

ARG TARGETOS
ARG TARGETARCH
RUN --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH \
    go build \
      -trimpath \
      -ldflags="-s -w -buildid=" \
      -o /out/jandibat-api \
      ./cmd/server && \
    CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH \
    go build -trimpath -ldflags="-s -w -buildid=" -o /out/jandibat-worker ./cmd/worker && \
    CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH \
    go build -trimpath -ldflags="-s -w -buildid=" -o /out/jandibat-maintenance ./cmd/maintenance

# Distroless intentionally has no shell or HTTP client. Copy the static
# BusyBox binary solely to provide an exec-form HTTP health probe.
FROM busybox:1.37.0-musl@sha256:fc6dddc4c44b1bfe37f41cae8e67d1693828e8f42a91862816d7953e2c9d3f23 AS healthcheck

FROM gcr.io/distroless/static-debian12:nonroot@sha256:1b7b9f0f0e0a1d2155f531db587cc48ec26aaf97ab64364225f5bf18a054e66a AS runtime

COPY --from=build --chown=nonroot:nonroot /out/jandibat-api /jandibat-api
COPY --from=build --chown=nonroot:nonroot /out/jandibat-worker /jandibat-worker
COPY --from=build --chown=nonroot:nonroot /out/jandibat-maintenance /jandibat-maintenance
COPY --from=healthcheck --chown=nonroot:nonroot /bin/busybox /busybox

ENV APP_ENV=production \
    API_ADDR=:8080

USER nonroot:nonroot

EXPOSE 8080

HEALTHCHECK --interval=30s --timeout=5s --start-period=10s --retries=3 \
  CMD ["/busybox", "wget", "--spider", "-q", "-T", "3", "http://127.0.0.1:8080/healthz"]

ENTRYPOINT ["/jandibat-api"]
