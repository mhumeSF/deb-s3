# deb-s3 Go Cheat Sheet

## Build and inspect

```bash
cd /Users/mike/Workspace/deb-s3-go
go build -o deb-s3 ./cmd/deb-s3
./deb-s3 --version
./deb-s3 --help
./deb-s3 COMMAND --help
```

## AWS authentication

Use the normal AWS environment variables:

```bash
export AWS_ACCESS_KEY_ID="..."
export AWS_SECRET_ACCESS_KEY="..."
export AWS_DEFAULT_REGION="us-east-1"
```

Alternatively, pass `--access-key-id`, `--secret-access-key`, and optionally
`--session-token`.

## Upload packages

```bash
./deb-s3 upload \
  --bucket my-bucket \
  --codename stable \
  --component main \
  package_1.0_amd64.deb
```

Useful upload variants:

```bash
# Preserve older versions
./deb-s3 upload --bucket my-bucket --preserve-versions package.deb

# Publish immutable SHA-256 by-hash indices
./deb-s3 upload --bucket my-bucket --by-hash package.deb

# Lock the repository during the update
./deb-s3 upload --bucket my-bucket --lock package.deb

# Update metadata without uploading the package file
./deb-s3 upload --bucket my-bucket --skip-package-upload package.deb
```

## Sign Release metadata

```bash
# Use the default GPG key
./deb-s3 upload --bucket my-bucket --sign package.deb

# Use a specific key
./deb-s3 upload --bucket my-bucket --sign=KEY_ID package.deb

# Sign with multiple keys
./deb-s3 upload \
  --bucket my-bucket \
  --sign=FIRST_KEY \
  --sign=SECOND_KEY \
  package.deb
```

Select another GPG executable or pass extra options:

```bash
./deb-s3 upload \
  --bucket my-bucket \
  --sign=KEY_ID \
  --gpg-provider gpg2 \
  --gpg-options '--pinentry-mode loopback' \
  package.deb
```

## List and inspect packages

```bash
./deb-s3 list --bucket my-bucket
./deb-s3 list --bucket my-bucket --arch amd64
./deb-s3 list --bucket my-bucket --long
```

Show one package record:

```bash
./deb-s3 show --bucket my-bucket package-name 1.2.3-1 amd64
```

Check whether one or more packages exist:

```bash
./deb-s3 exists \
  --bucket my-bucket \
  package-one package-two \
  1.2.3-1 \
  amd64
```

## Copy package metadata

```bash
./deb-s3 copy \
  --bucket my-bucket \
  --arch amd64 \
  package-name \
  testing \
  main
```

Copy selected versions:

```bash
./deb-s3 copy \
  --bucket my-bucket \
  --arch amd64 \
  --versions 1.0,1.1 \
  package-name testing main
```

## Delete package metadata

Delete all versions from one architecture:

```bash
./deb-s3 delete --bucket my-bucket --arch amd64 package-name
```

Delete selected versions:

```bash
./deb-s3 delete \
  --bucket my-bucket \
  --arch amd64 \
  --versions 1.2.3-1 \
  package-name
```

`delete` changes repository metadata but leaves `.deb` objects in the pool.

## Verify and repair

```bash
# Read-only verification
./deb-s3 verify --bucket my-bucket

# Remove manifest records whose package objects are missing
./deb-s3 verify --bucket my-bucket --fix-manifests

# Refresh and sign Release metadata
./deb-s3 verify --bucket my-bucket --sign=KEY_ID
```

## Clean unreferenced package objects

```bash
./deb-s3 clean --bucket my-bucket --lock
```

`clean` permanently deletes package objects that are not referenced by any
discovered manifest. Review the target bucket, prefix, and codename carefully.

## S3-compatible endpoints

```bash
./deb-s3 list \
  --bucket my-bucket \
  --endpoint https://objects.example.com \
  --force-path-style \
  --checksum-when-required \
  --visibility nil
```

Scope all repository keys beneath a prefix:

```bash
./deb-s3 list \
  --bucket my-bucket \
  --prefix repositories/project-a
```

## Common global options

| Option | Purpose |
| --- | --- |
| `--bucket`, `-b` | S3 bucket name; required |
| `--prefix` | Store all repository keys below a prefix |
| `--codename`, `-c` | Distribution codename; defaults to `stable` |
| `--component`, `-m` | Repository component; defaults to `main` |
| `--origin`, `-o` | Release file origin |
| `--suite` | Release file suite |
| `--by-hash` | Enable `Acquire-By-Hash` publication |
| `--sign[=KEY_ID]` | Sign Release metadata; repeatable |
| `--cache-control`, `-C` | Set object Cache-Control metadata |
| `--encryption`, `-e` | Enable AES-256 server-side encryption |
| `--visibility`, `-v` | Object ACL mode |
| `--quiet`, `-q` | Suppress informational output |

Most boolean options also have a `--no-OPTION` form.

## Tests and static checks

```bash
go test ./...
go test -race ./...
go vet ./...
go build ./...
```

## Build release artifacts

With `mise` installed:

```bash
mise install
VERSION=1.0.0 mise run build:deb
```

Generated under `dist/`:

```text
deb-s3_linux_amd64
deb-s3_linux_arm64
deb-s3_1.0.0_amd64.deb
deb-s3_1.0.0_arm64.deb
checksums.txt
```

Verify release checksums on Linux:

```bash
cd dist
sha256sum --check checksums.txt
```
