# Release and packaging

A release build produces static Linux binaries, Debian packages, and a
checksum file:

```text
dist/deb-s3_linux_amd64
dist/deb-s3_linux_arm64
dist/deb-s3_<version>_amd64.deb
dist/deb-s3_<version>_arm64.deb
dist/checksums.txt
```

## Building locally

```console
$ mise install
$ VERSION=26.1.1 mise run build:deb
$ cd dist
$ sha256sum --check checksums.txt
```

The binaries are statically linked (`CGO_ENABLED=0`) and cross-compiled for
Linux amd64 and arm64. The version is baked into the binary at build time; a
plain development build reports `devel`. Builds are reproducible: package
timestamps come from `SOURCE_DATE_EPOCH`, defaulting to the current commit
time, so set it explicitly when rebuilding an older release.

The Debian package installs a single file, `/usr/bin/deb-s3`. GnuPG is
recommended rather than required — you only need it if you use `--sign`.

## CI

Every push runs the unit tests (including the race detector), S3
configuration tests, `go vet`, both cross-builds, checksum verification, and
an install test of the amd64 package. Publishing a GitHub release runs the
same build, tests the amd64 package, and attaches the binaries, packages,
and checksum file as release assets.

Packages are built with [nFPM](https://nfpm.goreleaser.com/docs/configuration/),
whose version is pinned in `mise.toml` so release behavior does not change
silently.
