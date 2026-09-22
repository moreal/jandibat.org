FROM node:24.21.0-alpine@sha256:ebfe2f90462722a7a4de65e91990e97fe0d401c70e0e762c5b53302f905ec1c1 AS build

WORKDIR /workspace
RUN corepack enable

COPY package.json yarn.lock .yarnrc.yml ./
COPY apps/web/package.json ./apps/web/package.json
COPY apps/web/tools/openapi-typescript-cli/ ./apps/web/tools/openapi-typescript-cli/
COPY packages/contracts/package.json ./packages/contracts/package.json
COPY packages/custom-provider-sdk/package.json ./packages/custom-provider-sdk/package.json
RUN yarn install --immutable

COPY apps/web/ ./apps/web/
COPY packages/contracts/ ./packages/contracts/
RUN yarn workspace @jandibat/web build

FROM nginxinc/nginx-unprivileged:1.29-alpine@sha256:0c79d56aee561a1d81c63f00eee5fb5fe29279560cdc55e91425133104c7fbe6 AS runtime

ENV JANDIBAT_API_BASE_URL=""

COPY --from=build --chown=101:101 /workspace/apps/web/dist/client/ /usr/share/nginx/html/
COPY --chown=101:101 apps/web/nginx.conf /etc/nginx/conf.d/default.conf
COPY --chown=101:101 --chmod=755 apps/web/docker-entrypoint.d/40-runtime-config.sh /docker-entrypoint.d/40-runtime-config.sh

USER 101:101
EXPOSE 8080

HEALTHCHECK --interval=30s --timeout=3s --start-period=5s --retries=3 \
  CMD wget --quiet --tries=1 --spider http://127.0.0.1:8080/healthz || exit 1
