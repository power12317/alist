#!/usr/bin/env bash
set -euo pipefail

target="${1:?usage: fetch-musl-toolchain.sh TARGET DIRECTORY}"
directory="${2:?toolchain directory is required}"
case "$target" in
  linux-amd64-musl) filename=x86_64-linux-musl-cross.tgz ;;
  linux-arm64-musl) filename=aarch64-linux-musl-cross.tgz ;;
  linux-armv7l-musleabihf) filename=armv7l-linux-musleabihf-cross.tgz ;;
  *) printf 'Unsupported musl target: %s\n' "$target" >&2; exit 1 ;;
esac

mkdir -p "$directory"
archive="$directory/$filename"
for base in https://github.com/go-cross/musl-toolchain-archive/releases/latest/download https://musl.cc; do
  if curl --fail --location --silent --show-error --retry 4 --retry-all-errors --retry-delay 3 \
    --connect-timeout 20 --max-time 300 "$base/$filename" --output "$archive" \
    && tar -tzf "$archive" >/dev/null; then
    exit 0
  fi
done
printf 'Failed to download a valid toolchain for %s\n' "$target" >&2
exit 1
