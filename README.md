# deb-s3

`deb-s3` creates and manages APT repositories in Amazon S3 and compatible
object stores. It ships as a single static binary with no runtime
dependencies.

It is a Go port of the Ruby [deb-s3](https://github.com/deb-s3/deb-s3),
originally written by [Ken Robertson](https://github.com/krobertson/deb-s3).
The command-line surface and repository layout are compatible, so it can take
over a repository created and maintained by the Ruby tool.

## Build

```console
go build -o deb-s3 ./cmd/deb-s3
./deb-s3 --help
```

Release builds and Debian packages are described in
[docs/RELEASE.md](docs/RELEASE.md).
Container images are published to GHCR on releases (`X.Y.Z` tags) and on
every push to main (`edge`):

```console
docker run --rm ghcr.io/mhumesf/deb-s3:edge --help
```

## Quick start

```console
# Inspect a repository
deb-s3 list --bucket my-bucket --codename stable --component main --arch amd64

# Upload a package and publish by-hash indices
deb-s3 upload --bucket my-bucket --by-hash package_1.0_amd64.deb

# Upload and sign Release metadata
deb-s3 upload --bucket my-bucket --by-hash --sign=KEY_ID package_1.0_amd64.deb

# Remove metadata, then delete unreferenced pool objects
deb-s3 delete --bucket my-bucket --arch amd64 package-name
deb-s3 clean --bucket my-bucket
```

Run `deb-s3 COMMAND --help` for every option.

The default `--visibility public` matches the Ruby tool, but S3 buckets
created since April 2023 disable ACLs by default and reject it with
`AccessControlListNotSupported`. For such buckets pass `--visibility nil`,
which sends no ACL at all.

## Documentation

- [Command cheat sheet](docs/CHEATSHEET.md)
- [Manual test playbook](docs/TESTING.md)
- [GPG signing and publication order](docs/SIGNING.md)
- [Repository locking](docs/LOCKING.md)
- [Release and Debian packaging](docs/RELEASE.md)

Licensed under the MIT License. See [LICENSE](LICENSE).
