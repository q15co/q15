# syntax=docker/dockerfile:1.7

FROM golang:1.25-bookworm AS build

WORKDIR /src

COPY go.work go.work.sum ./
COPY libs ./libs
COPY systems ./systems

RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    cd /src/systems/proxy-service && \
    CGO_ENABLED=0 go build -o /out/q15-proxy-service .

FROM debian:bookworm-slim

RUN apt-get update && apt-get install -y --no-install-recommends \
    ca-certificates \
    && rm -rf /var/lib/apt/lists/*

COPY --from=build /out/q15-proxy-service /usr/local/bin/q15-proxy-service

WORKDIR /root

ENTRYPOINT ["/usr/local/bin/q15-proxy-service"]
