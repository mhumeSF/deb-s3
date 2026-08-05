# Repository locking

The Go port uses one lock object per codename:

```text
dists/<codename>/lockfile
```

The lock covers the Release file and every component and architecture below
that codename. Copy locks the destination codename because that is the
repository it mutates.

## Protocol

Acquisition writes a small JSON owner record with a unique random token, user,
host, process ID, and acquisition time. The write uses `PutObject` with
`If-None-Match: *`. Only one contender can create an absent key; existing keys
produce a precondition conflict. AWS documents that concurrent conditional
writes allow the first write to finish and reject later writes with a
precondition failure:

- <https://docs.aws.amazon.com/AmazonS3/latest/userguide/conditional-writes.html>

Contenders retry with bounded exponential backoff, report the current owner,
and stop promptly when their context is cancelled.

Release reads the owner token and then deletes the object with `If-Match` set
to the ETag observed during acquisition. This makes the ownership check and
delete atomic: a replaced lock has a different ETag and cannot be deleted by
the old handle. AWS documents ETag-conditional `DeleteObject` and its required
permissions here:

- <https://docs.aws.amazon.com/AmazonS3/latest/userguide/conditional-deletes.html>

Operation errors and context cancellation do not skip release; cleanup uses a
short context detached from the cancelled operation context.

## Endpoint requirements

Locking requires an object store that supports both:

- conditional create through `PutObject` with `If-None-Match: *`;
- conditional delete through `DeleteObject` with `If-Match: <etag>`.

AWS S3 general-purpose buckets support both operations. The credentials need
`s3:PutObject`, `s3:GetObject`, and `s3:DeleteObject` for the lock key.

S3-compatible endpoints must implement the same conditional request behavior.
There is deliberately no read-then-delete compatibility fallback because it
has a race that can delete a replacement owner's lock. An endpoint without
conditional delete fails `--lock` explicitly; commands remain usable without
`--lock`.

Locks do not expire or get stolen automatically. A stale lock should be
inspected and removed administratively after confirming that its recorded
owner is no longer active.
