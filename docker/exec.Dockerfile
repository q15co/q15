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

# The nix-patched binaries in this image carry their own interpreter, but a
# distro-built binary asks the FHS path for its architecture: /lib64 on the
# x86_64 multiarch layout, /lib everywhere else. The loader file name is
# therefore asked of nixpkgs for this build's own system instead of being
# written down, and only its directory is chosen here.
RUN nix --extra-experimental-features 'nix-command flakes' build --no-link \
        nixpkgs#tzdata nixpkgs#fontconfig.out \
        nixpkgs#dejavu_fonts nixpkgs#inter nixpkgs#liberation_ttf nixpkgs#noto-fonts \
        nixpkgs#nix-ld nixpkgs#glibc nixpkgs#gcc.cc.lib nixpkgs#zlib \
        nixpkgs#zstd.out nixpkgs#xz.out nixpkgs#bzip2.out \
        nixpkgs#libxml2.out nixpkgs#openssl.out nixpkgs#curl.out && \
    TZDATA_PATH="$(nix --extra-experimental-features 'nix-command flakes' eval --raw nixpkgs#tzdata.outPath)" && \
    FONTCONFIG_PATH="$(nix --extra-experimental-features 'nix-command flakes' eval --raw nixpkgs#fontconfig.out.outPath)" && \
    DEJAVU_PATH="$(nix --extra-experimental-features 'nix-command flakes' eval --raw nixpkgs#dejavu_fonts.outPath)" && \
    INTER_PATH="$(nix --extra-experimental-features 'nix-command flakes' eval --raw nixpkgs#inter.outPath)" && \
    LIBERATION_PATH="$(nix --extra-experimental-features 'nix-command flakes' eval --raw nixpkgs#liberation_ttf.outPath)" && \
    NOTO_PATH="$(nix --extra-experimental-features 'nix-command flakes' eval --raw nixpkgs#noto-fonts.outPath)" && \
    NIXLD_PATH="$(nix --extra-experimental-features 'nix-command flakes' eval --raw nixpkgs#nix-ld.outPath)" && \
    GLIBC_PATH="$(nix --extra-experimental-features 'nix-command flakes' eval --raw nixpkgs#glibc.outPath)" && \
    GCC_LIB_PATH="$(nix --extra-experimental-features 'nix-command flakes' eval --raw nixpkgs#gcc.cc.lib.outPath)" && \
    ZLIB_PATH="$(nix --extra-experimental-features 'nix-command flakes' eval --raw nixpkgs#zlib.outPath)" && \
    ZSTD_PATH="$(nix --extra-experimental-features 'nix-command flakes' eval --raw nixpkgs#zstd.out.outPath)" && \
    XZ_PATH="$(nix --extra-experimental-features 'nix-command flakes' eval --raw nixpkgs#xz.out.outPath)" && \
    BZIP2_PATH="$(nix --extra-experimental-features 'nix-command flakes' eval --raw nixpkgs#bzip2.out.outPath)" && \
    LIBXML2_PATH="$(nix --extra-experimental-features 'nix-command flakes' eval --raw nixpkgs#libxml2.out.outPath)" && \
    OPENSSL_PATH="$(nix --extra-experimental-features 'nix-command flakes' eval --raw nixpkgs#openssl.out.outPath)" && \
    CURL_PATH="$(nix --extra-experimental-features 'nix-command flakes' eval --raw nixpkgs#curl.out.outPath)" && \
    LD_PATH="$(nix --extra-experimental-features 'nix-command flakes' eval --raw nixpkgs#stdenv.cc.bintools.dynamicLinker)" && \
    LD_SO="${LD_PATH##*/}" && \
    case "${LD_SO}" in \
        ld-linux-x86-64.so.2) LD_DIR="/lib64" ;; \
        *) LD_DIR="/lib" ;; \
    esac && \
    mkdir -p /etc/fonts /etc/nix-ld/lib "${LD_DIR}" /var/lib/q15/bootstrap-nix && \
    ln -sfn "${TZDATA_PATH}/share/zoneinfo" /etc/zoneinfo && \
    ln -sfn "${FONTCONFIG_PATH}/etc/fonts/fonts.conf" /etc/fonts/fonts.conf && \
    printf '<?xml version="1.0"?>\n<!DOCTYPE fontconfig SYSTEM "fonts.dtd">\n<fontconfig>\n  <dir>%s/share/fonts</dir>\n  <dir>%s/share/fonts</dir>\n  <dir>%s/share/fonts</dir>\n  <dir>%s/share/fonts</dir>\n</fontconfig>\n' \
        "${DEJAVU_PATH}" "${INTER_PATH}" "${LIBERATION_PATH}" "${NOTO_PATH}" \
        > /etc/fonts/local.conf && \
    ln -sfn "${NIXLD_PATH}/libexec/nix-ld" "${LD_DIR}/${LD_SO}" && \
    ln -sfn "${GLIBC_PATH}/lib/${LD_SO}" /etc/nix-ld/ld && \
    ln -sfn "${GLIBC_PATH}/lib" /etc/nix-ld/lib/glibc && \
    ln -sfn "${GCC_LIB_PATH}/lib" /etc/nix-ld/lib/gcc && \
    ln -sfn "${ZLIB_PATH}/lib" /etc/nix-ld/lib/zlib && \
    ln -sfn "${ZSTD_PATH}/lib" /etc/nix-ld/lib/zstd && \
    ln -sfn "${XZ_PATH}/lib" /etc/nix-ld/lib/xz && \
    ln -sfn "${BZIP2_PATH}/lib" /etc/nix-ld/lib/bzip2 && \
    ln -sfn "${LIBXML2_PATH}/lib" /etc/nix-ld/lib/libxml2 && \
    ln -sfn "${OPENSSL_PATH}/lib" /etc/nix-ld/lib/openssl && \
    ln -sfn "${CURL_PATH}/lib" /etc/nix-ld/lib/curl && \
    test -d /etc/zoneinfo && \
    test -e /etc/fonts/fonts.conf && \
    test -e /etc/fonts/local.conf && \
    test -e "${LD_DIR}/${LD_SO}" && \
    test -e /etc/nix-ld/ld && \
    test -d /etc/nix-ld/lib/glibc && \
    test -d /etc/nix-ld/lib/gcc && \
    test -d /etc/nix-ld/lib/zlib && \
    test -d /etc/nix-ld/lib/zstd && \
    test -d /etc/nix-ld/lib/xz && \
    test -d /etc/nix-ld/lib/bzip2 && \
    test -d /etc/nix-ld/lib/libxml2 && \
    test -d /etc/nix-ld/lib/openssl && \
    test -d /etc/nix-ld/lib/curl && \
    cp -al /nix/. /var/lib/q15/bootstrap-nix/

ENV TZDIR=/etc/zoneinfo
ENV FONTCONFIG_FILE=/etc/fonts/fonts.conf
ENV NIX_LD=/etc/nix-ld/ld
ENV NIX_LD_LIBRARY_PATH=/etc/nix-ld/lib/glibc:/etc/nix-ld/lib/gcc:/etc/nix-ld/lib/zlib:/etc/nix-ld/lib/zstd:/etc/nix-ld/lib/xz:/etc/nix-ld/lib/bzip2:/etc/nix-ld/lib/libxml2:/etc/nix-ld/lib/openssl:/etc/nix-ld/lib/curl

COPY --from=build /out/q15-exec /usr/local/bin/q15-exec

WORKDIR /root

ENTRYPOINT ["/usr/local/bin/q15-exec"]
