# GPG Signing

The Go port supports signing on `upload`, `copy`, `delete`, and `verify`.
Passing `--sign` uses GPG's default signing key; passing `--sign=KEY_ID`
selects a key explicitly, and the option may be repeated. `--gpg-provider`
selects the executable and defaults to `gpg`.

## Process boundary

Signing runs the configured executable directly with an argument vector. It
does not invoke a command shell. `--gpg-options` supports whitespace, quoted
values, and backslash escapes, but deliberately performs no environment,
command, glob, or tilde expansion. SHA-256 is appended after extra options so
the required digest algorithm cannot be weakened by an earlier option.

The signer creates a private temporary directory, writes the exact rendered
`Release` bytes there, and asks GPG for both armored artifacts:

- `InRelease`, using a clear signature.
- `Release.gpg`, using a detached signature.

Both outputs must exist and be non-empty. The signer then asks the same GPG
provider to verify the clear signature and the detached signature before any
new Release generation is uploaded. Provider output is included in failures
to make missing keys, agent failures, and invalid options diagnosable.

## Publication order

Repository indices, including immutable by-hash objects, are published before
the Release generation. A signed generation is then uploaded in this order:

1. `Release`
2. `Release.gpg`
3. `InRelease`

Publishing `InRelease` last makes it the final commit object seen by clients
that prefer clear-signed metadata. If local signing or verification fails, no
object from the new Release/signature set is uploaded. An object-store failure
can still interrupt the three uploads; retaining immutable by-hash indices lets
clients holding the previous signed generation continue to resolve its index
contents.

An unsigned publication removes both a stale `Release.gpg` and a stale
`InRelease` before replacing `Release`, so an old `InRelease` can never keep
advertising a different repository generation.

## Tests

Deterministic tests use a fake executable to inspect literal arguments,
simulate errors and missing outputs, and assert upload ordering. When `gpg` is
available, an integration test creates a key in an isolated temporary
`GNUPGHOME`, signs both artifacts, and verifies them. The integration test is
skipped on development hosts without GPG; CI intended to validate release
signing should install GPG so it runs.
