#!/usr/bin/env bash
set -euo pipefail

case "$TARGET" in
  freebsd-amd64) arch=amd64; clang_target=x86_64-unknown-freebsd14.3 ;;
  freebsd-arm64) arch=arm64; clang_target=aarch64-unknown-freebsd14.3 ;;
  *) printf 'Unsupported FreeBSD target: %s\n' "$TARGET" >&2; exit 1 ;;
esac

sudo apt-get update -qq
sudo apt-get install -y --no-install-recommends clang lld
sysroot="$(mktemp -d)"
trap 'rm -rf "$sysroot"' EXIT

# Old releases are removed from download.freebsd.org, but kept in this archive.
curl --fail --location --retry 3 --connect-timeout 20 --max-time 300 \
  "https://archive.freebsd.org/old-releases/$arch/14.3-RELEASE/base.txz" \
  --output "$sysroot/base.txz"
tar -xJf "$sysroot/base.txz" -C "$sysroot" ./lib ./usr/lib ./usr/include
rm "$sysroot/base.txz"

export CGO_ENABLED=1 GOOS=freebsd GOARCH="$arch"
export CC="clang --target=$clang_target --sysroot=$sysroot"
export CGO_LDFLAGS='-fuse-ld=lld'
built_at="$(date -u +'%Y-%m-%dT%H:%M:%SZ')"
ldflags="-w -s -X github.com/alist-org/alist/v3/internal/conf.BuiltAt=$built_at"
ldflags+=" -X github.com/alist-org/alist/v3/internal/conf.GitCommit=$GITHUB_SHA"
ldflags+=" -X github.com/alist-org/alist/v3/internal/conf.Version=$RELEASE_TAG"
ldflags+=" -X github.com/alist-org/alist/v3/internal/conf.WebVersion=$WEB_VERSION"
mkdir -p build
go build -trimpath -tags=jsoniter -ldflags="$ldflags" -o "build/alist-$TARGET" .
