# syntax=docker/dockerfile:1.7

ARG NODE_VERSION=24.21.0
FROM node:${NODE_VERSION}-bookworm-slim AS ui
WORKDIR /src/systems/web/ui
COPY scripts/tool-versions.sh /src/scripts/tool-versions.sh
RUN set -eu; \
    . /src/scripts/tool-versions.sh; \
    test "$(node --version)" = "v${NODE_VERSION}"; \
    npm install --global "pnpm@${PNPM_VERSION}"
COPY systems/web/ui/package.json systems/web/ui/pnpm-lock.yaml systems/web/ui/pnpm-workspace.yaml ./
RUN --mount=type=cache,target=/root/.local/share/pnpm/store \
    set -eu; \
    . /src/scripts/tool-versions.sh; \
    test "$(node -p 'JSON.parse(require("fs").readFileSync("package.json")).packageManager')" = "pnpm@${PNPM_VERSION}"; \
    pnpm install --frozen-lockfile
COPY systems/web/ui ./
RUN set -eu; \
    pnpm build; \
    test -s /src/systems/web/internal/assets/dist/index.html

FROM golang:1.26-bookworm AS build
WORKDIR /src
COPY go.work go.work.sum ./
COPY libs ./libs
COPY systems ./systems
COPY --from=ui /src/systems/web/internal/assets/dist ./systems/web/internal/assets/dist
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    cd systems/web && CGO_ENABLED=0 go build -o /out/q15-web .

RUN mkdir /out/state && mkdir -m 0700 /out/state/q15-web

FROM gcr.io/distroless/static-debian12:nonroot
USER 65532:1000
COPY --from=build --chown=65532:1000 /out/state/ /var/lib/
COPY --from=build /out/q15-web /usr/local/bin/q15-web
EXPOSE 8080
ENTRYPOINT ["/usr/local/bin/q15-web"]
