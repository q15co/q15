# syntax=docker/dockerfile:1.7

FROM golang:1.25-bookworm AS build

WORKDIR /src

COPY go.work go.work.sum ./
COPY libs ./libs
COPY systems ./systems

RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    cd /src/systems/agent && \
    go build -o /out/q15-agent .

FROM debian:bookworm-slim

RUN apt-get update && apt-get install -y --no-install-recommends \
    ca-certificates \
    git \
    tzdata \
    && rm -rf /var/lib/apt/lists/*

COPY --from=build /out/q15-agent /usr/local/bin/q15-agent

ENV HOME=/root
WORKDIR /root

ENTRYPOINT ["/usr/local/bin/q15-agent"]
