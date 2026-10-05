# s3-object-gateway-go

一个本地运行、零外部依赖的 S3 兼容对象存储服务。数据保存在本机磁盘目录中，
通过标准 S3 REST 接口子集对外提供服务，可直接使用 AWS CLI、SDK 以及其他
SigV4 签名客户端访问。

## 运行

```bash
go run ./cmd/s3gw -addr 127.0.0.1:8333 -data ./data
# 或
make run
```

默认密钥可通过参数或环境变量覆盖：

| 参数 | 环境变量 | 默认值 |
| --- | --- | --- |
| `-access-key` | `S3_ACCESS_KEY` | `testaccesskey` |
| `-secret-key` | `S3_SECRET_KEY` | `testsecretkey` |
| `-abandoned-upload-age` | — | `24h`（超时分片上传回收阈值） |

使用 AWS CLI 访问：

```bash
export AWS_ACCESS_KEY_ID=testaccesskey
export AWS_SECRET_ACCESS_KEY=testsecretkey
export AWS_DEFAULT_REGION=us-east-1

aws --endpoint-url http://127.0.0.1:8333 s3 mb s3://demo
echo hello | aws --endpoint-url http://127.0.0.1:8333 s3 cp - s3://demo/hello.txt
aws --endpoint-url http://127.0.0.1:8333 s3 ls s3://demo/
aws --endpoint-url http://127.0.0.1:8333 s3 cp s3://demo/hello.txt -
aws --endpoint-url http://127.0.0.1:8333 s3api get-object --bucket demo --key hello.txt --range bytes=0-2 out
```

SDK 中把 endpoint 配置为 `http://127.0.0.1:8333`，并启用 path-style 寻址
（`S3ForcePathStyle` / `UsePathStyle: true`）。

## 支持的接口子集

- `GET /` — ListBuckets
- `PUT /{bucket}` / `DELETE /{bucket}` / `HEAD /{bucket}` — 创建 / 删除 / 检查桶
- `GET /{bucket}?list-type=2` — ListObjectsV2（`prefix`、`delimiter`、
  `max-keys`、`continuation-token`、`start-after`）
- `PUT /{bucket}/{key}` — PutObject（含 `x-amz-meta-*`、`Content-Type`、
  `Content-MD5`、`x-amz-content-sha256`）
- `GET /{bucket}/{key}` — GetObject，含单段 `Range: bytes=` 与条件请求头
  `If-Match` / `If-None-Match` / `If-Modified-Since` / `If-Unmodified-Since`
- `HEAD /{bucket}/{key}` — HeadObject（返回内容头与自定义元数据）
- `DELETE /{bucket}/{key}` — DeleteObject
- `PUT /{bucket}/{key}?uploads` — InitiateMultipartUpload
- `PUT /{bucket}/{key}?partNumber=N&uploadId=...` — UploadPart
- `POST /{bucket}/{key}?uploadId=...` — CompleteMultipartUpload
- `DELETE /{bucket}/{key}?uploadId=...` — AbortMultipartUpload
- `GET /{bucket}?uploads` — ListMultipartUploads
- `GET /{bucket}/{key}?uploadId=...` — ListParts
- `PUT /{bucket}/{key}` + `x-amz-copy-source` — CopyObject，支持
  `x-amz-copy-source-if-*` 全部四个条件头与 `x-amz-metadata-directive`

认证支持 Authorization 头形式的 SigV4 与查询串预签名 URL（presigned URL），
并接受 SDK 默认使用的 `STREAMING-AWS4-HMAC-SHA256-PAYLOAD`（aws-chunked）
流式签名载荷。时间偏差窗口为 15 分钟。

未实现的操作返回 `501 NotImplemented`（XML 错误体）；方法不允许返回
`405 MethodNotAllowed`。错误响应均为标准 S3 XML：
`SignatureDoesNotMatch`、`InvalidAccessKeyId`、`RequestTimeTooSkewed`、
`InvalidRange`（416）、`PreconditionFailed`（412）、`NoSuchBucket`、
`NoSuchKey`、`NoSuchUpload`、`EntityTooSmall`、`InvalidPart`、`BadDigest`、
`InvalidDigest`、`BucketAlreadyExists`、`BucketNotEmpty` 等。

## 磁盘布局与一致性语义

```
data/
  blobs/<xx>/<md5hex>         内容寻址、不可变的数据块（硬链接复用）
  blobs/tmp/                  崩溃遗留的临时写入，启动时清理
  buckets/<hex(bucket)>/
    objects/<hex(key)>.json   对象元数据（指向 blob），temp+fsync+rename 原子安装
    uploads/<uploadId>/upload.json   分片上传状态（parts 列表）
```

- **原子对象写入**：先写不可变 blob（fsync），再以 `write → fsync → rename`
  原子发布元数据；读取永远打开已提交版本对应的 blob，不会观察到半写内容，
  也不会出现内容与元数据跨版本混合。
- **并发同键写入**：同键写入经 per-key 锁串行化，收敛到其中一个完整版本；
  不同键之间完全并行。覆盖/删除后无引用的 blob 立即垃圾回收。
- **分片上传**：未完成的上传不出现在对象列举中、不可读取；Complete 在键锁内
  组装 blob 并原子发布；Abort 原子删除状态与分片。进程重启后从
  `upload.json` 重建上传状态，可继续上传或中止；超过
  `-abandoned-upload-age` 的上传由后台清理回收。
- **列举一致性**：每次 ListObjects 在存储读锁内复制键集合形成一致快照，
  单次请求分页基于同一快照；`continuation-token` 是词法标记（对象键或
  CommonPrefix），在并发写入/删除下持续有效，页内严格有序、不会重复。
- **崩溃恢复**：已确认对象（元数据与 blob 均落盘）在重启后完整保留；元数据
  指向缺失 blob 的损坏/中间状态在加载时被剔除，绝不暴露给读取方。

## 测试与验收

测试覆盖：SigV4 头签名 / 预签名 URL / 时间偏差 / 错误密钥、对象 CRUD 与
字节范围（含 416）、条件请求、自定义元数据与 Content-MD5、分片上传全流程
（含小分片拒绝、中止不可见、跨进程重启续传）、CopyObject 条件复制、列举
前缀/分隔符/翻页、并发写入版本收敛与无撕裂读、崩溃恢复与垃圾回收。

一键验收命令：

```bash
make acceptance
```

等价于 `go vet ./... && go test -race -count=1 ./...`。
