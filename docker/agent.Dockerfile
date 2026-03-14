# syntax=docker/dockerfile:1.7

FROM golang:1.25-bookworm AS build

WORKDIR /src

RUN apt-get update && apt-get install -y --no-install-recommends \
    gcc \
    libc6-dev \
    pkg-config \
    && rm -rf /var/lib/apt/lists/*

COPY go.work go.work.sum ./
COPY libs ./libs
COPY systems ./systems

RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    cd /src/systems/agent && \
    go build -o /out/q15 .

RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    cd /src/systems/sandbox-helper && \
    CGO_ENABLED=1 go build -tags='containers_image_openpgp exclude_graphdriver_btrfs' -o /out/q15-sandbox-helper .

FROM debian:bookworm-slim

RUN apt-get update && apt-get install -y --no-install-recommends \
    aardvark-dns \
    buildah \
    ca-certificates \
    crun \
    fuse-overlayfs \
    git \
    iptables \
    netavark \
    uidmap \
    && rm -rf /var/lib/apt/lists/*

COPY --from=build /out/q15 /usr/local/bin/q15
COPY --from=build /out/q15-sandbox-helper /usr/local/bin/q15-sandbox-helper

ENV BUILDAH_RUNTIME=/usr/bin/crun
ENV HOME=/root
WORKDIR /root

ENTRYPOINT ["/usr/local/bin/q15"]
CMD ["start", "--config", "/root/.config/q15/config.compose.toml"]
