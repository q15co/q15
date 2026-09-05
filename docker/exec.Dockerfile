# syntax=docker/dockerfile:1.7

FROM golang:1.26-bookworm AS build

WORKDIR /src

COPY go.work go.work.sum ./
COPY libs ./libs
COPY systems ./systems

RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    cd /src/systems/exec && \
    CGO_ENABLED=0 go build -o /out/q15-exec .

FROM nixos/nix:latest

RUN nix --extra-experimental-features 'nix-command flakes' build --no-link \
        nixpkgs#tzdata nixpkgs#fontconfig.out \
        nixpkgs#dejavu_fonts nixpkgs#inter nixpkgs#liberation_ttf nixpkgs#noto-fonts && \
    TZDATA_PATH="$(nix --extra-experimental-features 'nix-command flakes' eval --raw nixpkgs#tzdata.outPath)" && \
    FONTCONFIG_PATH="$(nix --extra-experimental-features 'nix-command flakes' eval --raw nixpkgs#fontconfig.out.outPath)" && \
    DEJAVU_PATH="$(nix --extra-experimental-features 'nix-command flakes' eval --raw nixpkgs#dejavu_fonts.outPath)" && \
    INTER_PATH="$(nix --extra-experimental-features 'nix-command flakes' eval --raw nixpkgs#inter.outPath)" && \
    LIBERATION_PATH="$(nix --extra-experimental-features 'nix-command flakes' eval --raw nixpkgs#liberation_ttf.outPath)" && \
    NOTO_PATH="$(nix --extra-experimental-features 'nix-command flakes' eval --raw nixpkgs#noto-fonts.outPath)" && \
    mkdir -p /etc/fonts /var/lib/q15/bootstrap-nix && \
    ln -sfn "${TZDATA_PATH}/share/zoneinfo" /etc/zoneinfo && \
    ln -sfn "${FONTCONFIG_PATH}/etc/fonts/fonts.conf" /etc/fonts/fonts.conf && \
    printf '<?xml version="1.0"?>\n<!DOCTYPE fontconfig SYSTEM "fonts.dtd">\n<fontconfig>\n  <dir>%s/share/fonts</dir>\n  <dir>%s/share/fonts</dir>\n  <dir>%s/share/fonts</dir>\n  <dir>%s/share/fonts</dir>\n</fontconfig>\n' \
        "${DEJAVU_PATH}" "${INTER_PATH}" "${LIBERATION_PATH}" "${NOTO_PATH}" \
        > /etc/fonts/local.conf && \
    test -d /etc/zoneinfo && \
    test -e /etc/fonts/fonts.conf && \
    test -e /etc/fonts/local.conf && \
    cp -al /nix/. /var/lib/q15/bootstrap-nix/

ENV TZDIR=/etc/zoneinfo
ENV FONTCONFIG_FILE=/etc/fonts/fonts.conf

COPY --from=build /out/q15-exec /usr/local/bin/q15-exec

WORKDIR /root

ENTRYPOINT ["/usr/local/bin/q15-exec"]
