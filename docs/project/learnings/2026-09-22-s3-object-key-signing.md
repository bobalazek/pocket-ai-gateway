# S3 object keys need their own signing representation

## Context and evidence

The first real local S3 recovery rehearsal rejected a backup with prefix `nightly +%/č`. The existing contract test checked for signing headers but did not validate them against an independent server. A regression test also showed that `path.Join` collapsed repeated slashes and dot segments, changing configured object keys.

## Change

The shared uploader preserves the key path and uses Go's query escaping with spaces converted to `%20` and encoded slashes restored. The resulting `RawPath` is used both on the wire and in the signature. This follows the [AWS SigV4 encoding and non-normalization rules](https://docs.aws.amazon.com/AmazonS3/latest/developerguide/sig-v4-header-based-auth.html) without adding a signing dependency.

## Verification and consequence

The regression failed before the fix and passed afterward for reserved punctuation, UTF-8, literal percent escapes, empty prefixes, and unnormalized keys. The full operations race suite passed. The optional Docker recovery test independently downloads and verifies the uploaded archive before restoring it through the production CLI. Local S3 interoperability is now evidence-backed; cloud credentials, TLS, retention policy, and off-host durability still require deployment-specific certification.
