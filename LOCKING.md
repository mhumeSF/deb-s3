# Repository locking

Pass `--lock` to stop two runs from updating the same repository at once.
Each codename gets one lock object:

```text
dists/<codename>/lockfile
```

The lock covers everything under that codename. `copy` locks the destination
codename, since that is the repository it changes.

## How it works

To take the lock, deb-s3 writes a small JSON record (who, where, when, plus a
random token) using a conditional write that only succeeds if the lock object
does not already exist. If someone else holds the lock, deb-s3 reports the
current owner and retries with backoff until it gets the lock or is
cancelled.

To release the lock, deb-s3 deletes it with a conditional delete tied to the
exact object it created, so it can never delete a lock that has since been
replaced by another run. The lock is released even when the operation fails
or is cancelled.

## Requirements

The object store must support S3 conditional writes and conditional deletes:

- [Conditional writes](https://docs.aws.amazon.com/AmazonS3/latest/userguide/conditional-writes.html)
- [Conditional deletes](https://docs.aws.amazon.com/AmazonS3/latest/userguide/conditional-deletes.html)

AWS S3 supports both, and the credentials need `s3:PutObject`,
`s3:GetObject`, and `s3:DeleteObject` on the lock key. Other S3-compatible
stores must support the same conditional requests — there is deliberately no
fallback, because an unconditional delete could remove someone else's lock.
On a store without support, `--lock` fails with a clear error; everything
else still works without `--lock`.

Locks never expire or get stolen automatically. If a run dies and leaves a
lock behind, check the recorded owner and delete the lock object by hand.
