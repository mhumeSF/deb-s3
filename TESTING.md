# Manual test playbook

A guided tour of every deb-s3 feature against a real bucket. Each scenario is
self-contained; run them top to bottom for a full demo. Everything is
sandboxed under an S3 prefix so it never touches real repository data, and the
final section deletes the sandbox.

## 0. Setup

Local requirements: `python3`, the `aws` CLI, and GnuPG for the signing
scenarios (`nix-shell -p gnupg` or `brew install gnupg`).

S3 requirements:

- A general-purpose bucket. The tool defaults to `us-east-1`; pass
  `--s3-region` (or set `AWS_DEFAULT_REGION`) if the bucket lives elsewhere —
  profile regions are deliberately ignored.
- Credentials granting `s3:GetObject`, `s3:PutObject`, and `s3:DeleteObject`
  on the sandbox prefix, plus `s3:ListBucket` on the bucket. `ListBucket` is
  required even without `clean`: without it S3 answers 403 instead of 404 for
  missing keys, which breaks bootstrapping a fresh repository.
- `s3:PutObjectAcl` only if you use a `--visibility` other than `nil`; ACL
  puts fail on buckets created with ACLs disabled (the AWS default).
- `--lock`, `--fail-if-exists`, and `--by-hash` rely on S3 conditional writes
  (`If-None-Match`/`If-Match`). AWS supports these; older S3-compatible
  stores behind `--endpoint` may not.

```console
go build -o deb-s3 ./cmd/deb-s3
python3 demo/make-debs.py            # writes demo/fixtures/

export BUCKET=your-demo-bucket        # e.g. aws s3 mb s3://$BUCKET --region us-east-1
alias ds3='./deb-s3 --bucket $BUCKET --prefix deb-s3-demo --visibility nil --codename noble'
alias s3ls='aws s3 ls s3://$BUCKET/deb-s3-demo/'
```

`--visibility nil` skips object ACLs, which fail on buckets created with ACLs
disabled (the AWS default since 2023). `--prefix deb-s3-demo` is the sandbox.

## 1. First upload and the read commands

```console
ds3 upload demo/fixtures/demo_1.0-1_amd64.deb
ds3 list                                  # demo 1.0-1 amd64
ds3 list --long                           # full Packages paragraph
ds3 show demo 1.0-1 amd64                 # single package record
ds3 exists demo 1.0-1 amd64; echo $?      # Found, exit 0
ds3 exists demo 9.9-9 amd64; echo $?      # Missing, exit 1
s3ls --recursive                          # dists/ metadata + pool/ object
```

## 2. Malformed packages are rejected

Each fixture fails with a different, specific error; nothing is uploaded:

```console
for bad in demo/fixtures/bad-*.deb; do echo "== $bad"; ds3 upload "$bad"; done
```

| Fixture | Expected error |
|---|---|
| `bad-empty.deb` | missing ar magic |
| `bad-garbage.deb` | incorrect ar magic |
| `bad-first-member.deb` | first member is "hello.txt" instead of debian-binary |
| `bad-format-version.deb` | unsupported Debian package format version "3.0" |
| `bad-missing-data.deb` | Debian package is missing data archive |
| `bad-compression.deb` | unsupported control archive compression in "control.tar.lzma" |
| `bad-no-control-file.deb` | Debian package is missing control file |
| `bad-truncated.deb` | read control file: unexpected EOF |

## 3. GPG signing

Create a throwaway key in an isolated keyring:

```console
export GNUPGHOME="$(mktemp -d)"; chmod 700 "$GNUPGHOME"
gpg --batch --passphrase '' --quick-gen-key "Deb S3 Demo <demo@example.test>" ed25519 sign never
KEYID=$(gpg --list-keys --with-colons | awk -F: '/^pub/{print $5; exit}')
```

Sign, then verify both signature artifacts like an APT client would:

```console
ds3 upload --sign=$KEYID demo/fixtures/demo_1.0-1_amd64.deb

aws s3 cp s3://$BUCKET/deb-s3-demo/dists/noble/InRelease - | gpg --verify        # clear-signed
gpg --verify \
  <(aws s3 cp s3://$BUCKET/deb-s3-demo/dists/noble/Release.gpg -) \
  <(aws s3 cp s3://$BUCKET/deb-s3-demo/dists/noble/Release -)                    # detached
```

Signing with an unknown key fails before anything is published:

```console
ds3 upload --sign=DEADBEEF demo/fixtures/demo_1.0-1_amd64.deb   # !! sign Release: create InRelease
```

An unsigned publish removes stale signatures so clients can never validate an
old repository generation against a new index:

```console
ds3 upload demo/fixtures/demo_1.0-1_amd64.deb
aws s3 ls s3://$BUCKET/deb-s3-demo/dists/noble/ | grep -c "InRelease\|Release.gpg"   # 0
```

## 4. Versions, upgrades, and preserve-versions

```console
ds3 upload demo/fixtures/demo_1.1-1_amd64.deb
ds3 list                                              # only 1.1-1 (default: replace)
ds3 upload --preserve-versions demo/fixtures/demo_1.0-1_amd64.deb
ds3 list                                              # 1.1-1 and 1.0-1
ds3 delete demo --arch amd64 --versions 1.0-1         # delete one version
ds3 delete demo --arch amd64                          # delete all (prints a warning)
ds3 delete demo --arch amd64                          # !! No packages were deleted.
```

## 5. fail-if-exists conflict detection

`demo/fixtures/conflict/demo_1.0-1_amd64.deb` is the same name and version
with different contents:

```console
ds3 upload --fail-if-exists demo/fixtures/demo_1.0-1_amd64.deb            # ok
ds3 upload --fail-if-exists demo/fixtures/demo_1.0-1_amd64.deb            # ok again: byte-identical is idempotent
ds3 upload --fail-if-exists demo/fixtures/conflict/demo_1.0-1_amd64.deb   # !! already exists with different contents
```

## 6. Architecture handling

An `Architecture: all` package fans out into every architecture manifest:

```console
ds3 upload demo/fixtures/demo-all_2.0_all.deb
ds3 list --arch amd64        # demo-all appears alongside amd64 packages
aws s3 ls s3://$BUCKET/deb-s3-demo/dists/noble/main/    # binary-amd64 ... binary-all
```

`--arch` can place an `Architecture: all` package into one specific manifest
instead of fanning it out, but refuses to publish a concrete architecture
under a different one (that would advertise a binary that cannot run there):

```console
ds3 upload --arch amd64 demo/fixtures/demo-all_2.0_all.deb   # ok: lands only in binary-amd64
ds3 upload --arch arm64 demo/fixtures/demo_1.1-1_amd64.deb   # !! package ... is Architecture: amd64 and cannot be uploaded with --arch arm64
```

## 7. Folded control fields and epochs

`demo-tags` carries a multi-line `Description` and a folded `Tag:` field, and
`demo-epoch` has version `1:2.0-1`:

```console
ds3 upload demo/fixtures/demo-tags_1.0-1_amd64.deb demo/fixtures/demo-epoch_2.0-1_amd64.deb
ds3 list --long          # Tag: folds across continuation lines; epoch renders as 1:2.0-1
ds3 show demo-epoch 1:2.0-1 amd64
```

Re-publishing round-trips the folded fields byte-for-byte (re-run `list
--long` after any later upload and compare).

## 8. copy across codenames, then clean safely

`copy` is metadata-only: the destination codename references the source
codename's pool objects. `clean` accounts for that:

```console
ds3 upload demo/fixtures/demo_1.0-1_amd64.deb
ds3 copy demo jammy main --arch amd64
./deb-s3 --bucket $BUCKET --prefix deb-s3-demo --visibility nil --codename jammy list

ds3 delete demo --arch amd64      # remove from noble; jammy still references the pool file
ds3 clean                         # cleans pool/noble but keeps demo_1.0-1 (jammy needs it)
aws s3 ls s3://$BUCKET/deb-s3-demo/pool/noble/d/de/

./deb-s3 --bucket $BUCKET --prefix deb-s3-demo --visibility nil --codename jammy delete demo --arch amd64
ds3 clean                         # now nothing references it; the pool file is deleted
```

## 9. verify and repair

Simulate a lost pool object, then detect and repair it:

```console
ds3 upload demo/fixtures/demo_1.0-1_amd64.deb
aws s3 rm s3://$BUCKET/deb-s3-demo/pool/noble/d/de/demo_1.0-1_amd64.deb
ds3 verify                        # prints the missing package record
ds3 verify --fix-manifests        # removes it from the manifest and republishes
ds3 list                          # gone
```

## 10. Repository locking

Plant a foreign lock, watch the upload wait with backoff, then release it
from a second terminal:

```console
echo '{"token":"manual-demo","user":"someone-else","host":"their-laptop"}' \
  | aws s3 cp - s3://$BUCKET/deb-s3-demo/dists/noble/lockfile

ds3 upload --lock demo/fixtures/demo_1.0-1_amd64.deb
# Repository is locked by another user: someone-else at host their-laptop
# Attempting to obtain a lock after ...

# second terminal:
aws s3 rm s3://$BUCKET/deb-s3-demo/dists/noble/lockfile
# first terminal acquires the lock, uploads, prints ">> Lock released."
```

## 11. by-hash indexes and metadata-only uploads

```console
ds3 upload --by-hash demo/fixtures/demo_1.0-1_amd64.deb
aws s3 ls s3://$BUCKET/deb-s3-demo/dists/noble/main/binary-amd64/by-hash/SHA256/
aws s3 cp s3://$BUCKET/deb-s3-demo/dists/noble/Release - | grep Acquire-By-Hash

ds3 upload --skip-package-upload demo/fixtures/demo_1.1-1_amd64.deb   # manifests only
ds3 verify                                                            # reports 1.1-1 missing from the pool
```

## 12. Point a real APT client at it (optional)

On any Ubuntu/Debian host or container with access to the bucket:

```console
gpg --export --armor $KEYID > demo.asc            # from the machine holding the demo key
# on the client:
sudo install -m 0644 demo.asc /etc/apt/keyrings/deb-s3-demo.asc
echo "deb [signed-by=/etc/apt/keyrings/deb-s3-demo.asc] https://$BUCKET.s3.amazonaws.com/deb-s3-demo noble main" \
  | sudo tee /etc/apt/sources.list.d/deb-s3-demo.list
sudo apt-get update && apt-get download demo
```

(Requires the objects to be publicly readable or fronted by something that
signs requests; with a private bucket, use `apt-transport-s3` or a proxy.)

## 13. Cleanup

```console
aws s3 rm --recursive s3://$BUCKET/deb-s3-demo/
rm -rf "$GNUPGHOME" demo/fixtures
```
