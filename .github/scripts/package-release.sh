#!/usr/bin/env bash
set -euo pipefail

target="${1:?usage: package-release.sh TARGET}"
case "$target" in
  *[!a-z0-9-]*|'') printf 'Invalid build target: %s\n' "$target" >&2; exit 1 ;;
esac

executable=alist
extension=tar.gz
if [[ "$target" == windows-* ]]; then
  executable=alist.exe
  extension=zip
fi
binary="build/alist-$target"
[[ "$executable" != alist.exe ]] || binary+=.exe
test -s "$binary"

mkdir -p build/packages
output_dir="$(cd build/packages && pwd)"
stage="$(mktemp -d)"
trap 'rm -rf "$stage"' EXIT
install -m 755 "$binary" "$stage/$executable"
install -m 644 LICENSE "$stage/LICENSE"
archive="$output_dir/alist-$target.$extension"
if [[ "$extension" == zip ]]; then
  (cd "$stage" && zip -q "$archive" "$executable" LICENSE)
else
  COPYFILE_DISABLE=1 tar -czf "$archive" -C "$stage" "$executable" LICENSE
fi
printf 'Package: %s\n' "$archive"
