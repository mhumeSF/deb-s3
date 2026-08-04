# Migrating from Ruby to Go

The Go executable uses the same command names, repository object layout, and
global configuration model as the Ruby implementation. Existing repositories
do not require an offline conversion. Treat the switch as a publisher change:
only one implementation should mutate a given codename at a time.

## Preview safely

Keep the Ruby command at its existing path and install the downloaded Go
binary under a temporary name:

```console
$ install -m 0755 deb-s3_linux_amd64 "$HOME/bin/deb-s3-go"
$ deb-s3-go --version
```

Run read-only comparisons against each repository shape you use:

```console
$ deb-s3 list --bucket example --codename stable --component main --arch amd64 >ruby.list
$ deb-s3-go list --bucket example --codename stable --component main --arch amd64 >go.list
$ diff -u ruby.list go.list
```

Repeat for custom endpoints, prefixes, credentials, and `--long` output where
applicable. The Go golden fixture was captured from the Ruby implementation and
asserts byte-for-byte rendering, while the storage tests record S3 requests
without contacting AWS.

Before the first mutating run:

1. Enable bucket versioning or take a scoped backup of `dists/<codename>/`.
2. Stop Ruby publishers for that codename.
3. Run one Go update with `--lock`; keep the same signing and by-hash choices
   for all concurrent publishers.
4. Fetch `Release` or `InRelease` and the referenced `Packages` files as an APT
   client would.
5. Move remaining publishers only after the canary repository is healthy.

Do not use `clean` as a canary operation: it is intentionally destructive to
unreferenced pool objects.

## Deliberate differences

- The Go reader parses `.deb` ar/tar contents natively, including gzip, xz,
  zstd, and bzip2 control archives. It does not invoke `dpkg`, `ar`, or `tar`.
- `--by-hash` publishes immutable SHA-256 index objects and advertises
  `Acquire-By-Hash: yes`. It is opt-in so migration does not silently change an
  existing repository.
- Go locking uses S3 conditional create and ownership-safe conditional delete.
  A custom endpoint must implement those conditions correctly before `--lock`
  is enabled.
- GPG is executed directly, without a shell. Quoting in `--gpg-options` is
  tokenized, but shell variables, command substitutions, globs, and tildes are
  never expanded.
- Signed publication uploads `Release`, then `Release.gpg`, and commits with
  `InRelease` last. Unsigned publication removes both stale signature objects;
  Ruby removed only `Release.gpg` and could leave an obsolete `InRelease`.
- The Debian package contains a native executable and does not install Ruby,
  Thor, or the AWS Ruby SDK. GnuPG remains optional unless signing is used.

## Rollback

Stop Go publishers first. Restore the previous `dists/<codename>/` generation
from bucket versioning or the scoped backup if the Go run changed metadata you
do not want to retain. Then reinstall or expose the Ruby command:

```console
$ sudo apt-get remove deb-s3
$ PREVIOUS_VERSION=26.1.1
$ gem install deb-s3 --version "${PREVIOUS_VERSION}"
$ hash -r
$ deb-s3 help
```

Package objects uploaded to `pool/` are harmless if no manifest references
them; leave them in place during rollback and review them later. Do not run
either implementation concurrently until every lock holder and scheduled job
from the other implementation has stopped.
