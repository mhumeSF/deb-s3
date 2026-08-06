# deb-s3

`deb-s3` creates and manages APT repositories in Amazon S3 and compatible
object stores. It ships as a single static binary with no runtime
dependencies.

## Build

```console
go build -o deb-s3 ./cmd/deb-s3
./deb-s3 --help
```

Release builds and Debian packages are described in [RELEASE.md](RELEASE.md).

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

## Documentation

- [Manual test playbook](TESTING.md)
- [GPG signing and publication order](SIGNING.md)
- [Repository locking](LOCKING.md)
- [Release and Debian packaging](RELEASE.md)

Licensed under the MIT License. See [LICENSE](LICENSE).
