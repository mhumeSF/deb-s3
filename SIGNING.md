# GPG signing

Pass `--sign` on `upload`, `copy`, `delete`, or `verify` to sign the
repository's Release metadata. `--sign` alone uses your default GPG key;
`--sign=KEY_ID` picks a key and can be repeated to sign with several keys.
`--gpg-provider` chooses the GPG executable (default `gpg`).

## How signing runs

deb-s3 runs GPG directly, never through a shell. `--gpg-options` accepts
quoted values and escapes, but nothing is shell-expanded, and SHA-256 is
always enforced as the digest algorithm regardless of extra options.

For each publish, deb-s3 writes the Release file to a private temporary
directory and produces both signature formats:

- `InRelease` — clear-signed copy of Release
- `Release.gpg` — detached signature

Both signatures are verified locally before anything is uploaded. If signing
or verification fails, nothing new is published, and the GPG output is
included in the error so problems like missing keys are easy to spot.

## Publication order

Package indices are uploaded first, then the signed set in this order:
`Release`, then `Release.gpg`, then `InRelease`. Clients treat `InRelease`
as the final word, so it goes last. With `--by-hash`, clients that already
fetched the previous signed generation keep working even if an upload is
interrupted partway through.

Publishing without `--sign` deletes any stale `Release.gpg` and `InRelease`,
so an old signature can never keep describing a repository that has moved on.

## Tests

Unit tests use a fake GPG executable to check arguments, error handling, and
upload order. When real `gpg` is installed, an integration test signs and
verifies with a throwaway key in an isolated `GNUPGHOME`; it is skipped when
GPG is missing, so CI that validates release signing should install GPG.
