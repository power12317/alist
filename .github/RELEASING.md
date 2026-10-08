# Publishing this fork

The `release` workflow builds and publishes packages when a version tag ending in
`-mod` is pushed. The version number follows the latest upstream release; for
example, upstream `v3.64.0` becomes `v3.64.0-mod` in this repository.

```sh
git switch main
git tag -a v3.64.0-mod -m 'AList v3.64.0 modified release'
git push origin main
git push origin v3.64.0-mod
```

No local GitHub CLI login or personal access token is required. The workflow uses
the short-lived `GITHUB_TOKEN` supplied by GitHub Actions. Only the publishing job
requests `contents: write` to create the Release and upload its attachments.
GitHub Actions must be enabled for the repository; a Git push cannot change that
repository setting.

The workflow validates the Google Drive share driver, fetches one released
frontend for all builds, then creates these packages with CGO enabled:

| System | Architectures |
| --- | --- |
| Windows | amd64, arm64 |
| macOS | amd64, arm64 |
| Linux (musl) | amd64, arm64, ARMv7 hard-float |
| Android | amd64, arm64 |
| FreeBSD | amd64, arm64 |

Windows packages are ZIP archives containing `alist.exe` and `LICENSE`. Other
platforms use tar.gz archives containing an executable `alist` and `LICENSE`.
`SHA256SUMS.txt` covers all 11 packages. The Release is published only after all
builds succeed and the attachments have been uploaded. Intermediate packages are
also available as Actions artifacts for 14 days.

Go is selected from `go.mod`. FreeBSD builds use the official archived 14.3
sysroot, because the regular download server removes superseded releases.

For build corrections before publishing, rerun failed jobs on the same tag if
the source is unchanged. For source changes, create a new version such as
`v3.64.0-mod.1`; do not move an already published tag.

Ordinary pushes to `main` and `fix_local_perm` run `build` and upload development
binaries to Actions artifacts. They do not publish a Release. Upstream-only
workflows that update the shared beta tag, push to upstream frontend/desktop
repositories, or publish to the upstream Docker Hub account are guarded so they
do not run in this fork.
