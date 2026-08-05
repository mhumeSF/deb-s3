# Release and Packaging

The Go release build produces four installable artifacts and a checksum file:

```text
dist/deb-s3_linux_amd64
dist/deb-s3_linux_arm64
dist/deb-s3_<version>_amd64.deb
dist/deb-s3_<version>_arm64.deb
dist/checksums.txt
```

## Local build

Install the pinned tools and run the mise task from the repository root:

```console
$ mise install
$ VERSION=26.1.1 mise run build:deb
$ cd dist
$ sha256sum --check checksums.txt
```

The binaries are statically linked (`CGO_ENABLED=0`) and cross-compiled for
Linux amd64 and arm64. The version is injected into
`internal/buildinfo.Version`; an ordinary development build reports `devel`.
Release builds use `-trimpath`, omit VCS metadata, and set nFPM timestamps from
`SOURCE_DATE_EPOCH`, defaulting to the current Git commit time. Set
`SOURCE_DATE_EPOCH` explicitly when reproducing an older build.

The Debian package installs only `/usr/bin/deb-s3`. GnuPG is a recommended rather than required package because only
repositories using `--sign` need it.

## Automated verification

The CI workflow runs:

- Go unit tests and the race detector.
- Explicit AWS-style and custom-endpoint S3 configuration tests.
- `go vet` and a complete Go build.
- Both cross-builds, checksum verification, Debian control inspection, amd64
  package installation, and a version assertion.

Publishing a GitHub release runs the same build task, installs and tests the
amd64 Debian package, preserves both `.deb` files as workflow artifacts, and
uploads the binaries, packages, and checksum file as release assets.

The package definition follows the current [nFPM configuration
reference](https://nfpm.goreleaser.com/docs/configuration/), and the nFPM
version is pinned in `mise.toml` so release behavior does not change silently.
