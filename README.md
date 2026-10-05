# s3-object-gateway-go

A locally running, dependency-free object storage server that exposes a
subset of the **Amazon S3 REST API**. Data is stored on the local disk; no
external services are required. Authentication (AWS **Signature V4**),
conditional semantics and error responses follow the S3 protocol so common
S3 tools and SDKs can talk to it directly.

## Run

```sh
go build -o bin/s3gw ./cmd/server
./bin/s3gw -addr :9000 -data ./.s3gw-data \
  -access-key testaccesskey -secret-key testsecretkey
```

All flags may also be supplied through environment variables:
`S3GW_ADDR`, `S3GW_DATA`, `S3GW_ACCESS_KEY`, `S3GW_SECRET_KEY`.

Point an S3 client at `http://127.0.0.1:9000` with path-style addressing and
any region (for example `us-east-1`). The server exposes a single bucket
named `local`; object keys are taken from the request path.

Example with the AWS CLI:

```sh
aws --endpoint-url http://127.0.0.1:9000 s3 cp ./file.txt s3://local/file.txt
aws --endpoint-url http://127.0.0.1:9000 s3 ls s3://local/
```

## Supported API subset

| Operation | HTTP |
| --- | --- |
| List buckets | `GET /` |
| List objects v1 / v2 (prefix, delimiter, marker / continuation-token, max-keys) | `GET /?list-type=2` |
| Delete multiple objects | `POST /?delete` |
| Put object (incl. custom metadata & content headers) | `PUT /<key>` |
| Get object, byte-range reads (`Range`) | `GET /<key>` |
| Head object | `HEAD /<key>` |
| Delete object | `DELETE /<key>` |
| Copy object (incl. copy-source conditional headers, metadata directive) | `PUT /<dst>` with `x-amz-copy-source` |
| Initiate multipart upload | `POST /<key>?uploads` |
| Upload part (incl. `aws-chunked` streaming signatures) | `PUT /<key>?partNumber=&uploadId=` |
| Complete multipart upload | `POST /<key>?uploadId=` |
| Abort multipart upload | `DELETE /<key>?uploadId=` |
| List parts | `GET /<key>?uploadId=` |
| List in-progress multipart uploads | `GET /?uploads` |

Recognized headers: `Content-Type`, `Content-Encoding`, `Content-Language`,
`Content-Disposition`, `Cache-Control`, `Expires`, and any
`X-Amz-Meta-*` user metadata. These are stored and returned verbatim.

Unsupported operations/sub-resources (e.g. ACLs, versioning, bucket
configuration, server-side encryption requests, object lock) return a
well-formed S3 XML error with the appropriate status
(`501 NotImplemented`, `400 InvalidArgument`, `405 MethodNotAllowed`) —
they never succeed silently.

## Authentication

Every request must be signed with AWS SigV4, either via the
`Authorization` header or presigned query parameters.

- Wrong signature / malformed header → `403 SignatureDoesNotMatch`.
- Unknown access key → `403 InvalidAccessKeyId`.
- Request timestamp outside the 15-minute window, or an expired presigned
  URL → `403 RequestTimeTooSkewed`.
- Bodies are integrity-checked: exact `x-amz-content-sha256` hashes,
  `Content-MD5`, and per-chunk signatures for
  `STREAMING-AWS4-HMAC-SHA256-PAYLOAD` (`aws-chunked`, used by the AWS CLI)
  are verified as the body is read; a mismatch yields
  `403 SignatureDoesNotMatch`.

## Consistency & durability semantics

- **Atomic object writes.** Each put first streams and `fsync`s the full
  body to an immutable, versioned blob. The object becomes visible only when
  the manifest is atomically replaced (write-to-temp + `rename`). Readers
  therefore never observe a partially written object.
- **Concurrent writes converge.** Mutations are serialized on the manifest.
  Several concurrent puts to the same key end with exactly one complete
  version whose size/ETag/metadata all describe the same bytes — never a
  mixture of versions.
- **Range reads.** Satisfiable `Range` requests return `206` with a correct
  `Content-Range`; malformed or unsatisfiable ranges return
  `416 InvalidRange`.
- **Consistent listing.** Each list page is computed from a single
  snapshot, so concurrent puts/deletes while paging never duplicate or drop a
  key, and the returned marker/continuation token remains valid.
- **Multipart uploads.** Incomplete uploads are never readable or listable
  as objects. Completion assembles and fsyncs the whole object before
  publishing it in one step; abort removes the upload and all its parts
  atomically.
- **Restart & recovery.** Confirmed objects survive process restart. On
  startup the server reconciles its directories: staged scratch data and
  orphaned blobs/parts are removed, while in-progress multipart uploads with
  valid records are retained and can be continued, completed or aborted.
  Parts left by aborted, timed-out, or crashed clients are also reclaimed by
  a periodic sweep (uploads older than 24h).
- **Copy** is committed atomically; a failed copy leaves no partial content
  or orphan metadata.

## On-disk layout

```
<data-dir>/
  manifest.json        # durable index: key -> version record
  data/<version>.dat   # immutable object blobs
  uploads/<upload-id>/ # multipart upload record + parts
  tmp/                 # staging files (cleared on startup)
```

## Acceptance

Run the full automated acceptance suite (unit + signed end-to-end HTTP
tests, including the race detector) with:

```sh
make accept
```

which is equivalent to:

```sh
go build ./... && go vet ./... && go test -race -count=1 ./...
```
