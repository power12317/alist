#!/usr/bin/env bash
set -euo pipefail

# Fetch once so every platform embeds the same released frontend.
work_dir="$(mktemp -d)"
trap 'rm -rf "$work_dir"' EXIT

gh api repos/AlistGo/alist-web/releases/latest > "$work_dir/release.json"
web_version="$(jq -er '.tag_name' "$work_dir/release.json")"
web_url="$(jq -er '.assets[] | select(.name == "dist.tar.gz") | .browser_download_url' "$work_dir/release.json")"
curl --fail --location --retry 3 --connect-timeout 20 --max-time 180 \
  "$web_url" --output "$work_dir/dist.tar.gz"
tar -xzf "$work_dir/dist.tar.gz" -C "$work_dir"
test -s "$work_dir/dist/index.html"
mkdir -p public/dist
cp -R "$work_dir/dist/." public/dist/
printf 'web_version=%s\n' "$web_version" >> "$GITHUB_OUTPUT"
printf 'Frontend: %s\n' "$web_version"
