# syntax=docker/dockerfile:1.7

FROM node:24-bookworm-slim AS ui
WORKDIR /src/systems/web
COPY systems/web ./
# Before #184 introduces the UI, retain only the placeholder. Once a UI exists,
# its pinned build must write internal/assets/dist; failures are never hidden.
RUN set -eu; \
    corepack enable; \
    if [ -f ui/package.json ]; then \
      cd ui; \
      node -e 'if (!/^pnpm@[0-9]+\.[0-9]+\.[0-9]+/.test(require("./package.json").packageManager)) throw new Error("ui/package.json must pin pnpm");'; \
      pnpm install --frozen-lockfile; \
      pnpm build; \
      test -s /src/systems/web/internal/assets/dist/index.html; \
    fi

FROM golang:1.26-bookworm AS build
WORKDIR /src
COPY go.work go.work.sum ./
COPY libs ./libs
COPY systems ./systems
COPY --from=ui /src/systems/web/internal/assets/dist ./systems/web/internal/assets/dist
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    cd systems/web && CGO_ENABLED=0 go build -o /out/q15-web .

FROM gcr.io/distroless/static-debian12:nonroot
USER 65532:1000
COPY --from=build /out/q15-web /usr/local/bin/q15-web
EXPOSE 8080
ENTRYPOINT ["/usr/local/bin/q15-web"]
