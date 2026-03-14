# syntax=docker/dockerfile:1.7

FROM golang:1.25-bookworm AS build

WORKDIR /src

COPY go.work go.work.sum ./
COPY libs ./libs
COPY systems ./systems

RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    cd /src/systems/exec-service && \
    CGO_ENABLED=0 go build -o /out/q15-exec-service .

FROM nixos/nix:latest

COPY --from=build /out/q15-exec-service /usr/local/bin/q15-exec-service

WORKDIR /root

ENTRYPOINT ["/usr/local/bin/q15-exec-service"]
CMD ["serve", "--listen", "0.0.0.0:50051", "--workspace-dir", "/workspace", "--memory-dir", "/memory", "--skills-dir", "/skills"]
