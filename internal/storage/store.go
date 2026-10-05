package storage

import (
	"context"
	"crypto/md5"
	"encoding/base64"
	"encoding/hex"
	"io"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/guoyyit/s3-object-gateway-go/internal/protocol"
)

// ObjectInfo holds the metadata of one committed object version.
type ObjectInfo struct {
	Bucket       string
	Key          string
	Size         int64
	ETag         string
	ContentType  string
	LastModified time.Time
	UserMetadata map[string]string
	BlobID       string
}

// PutOptions carries headers and metadata for an object write.
type PutOptions struct {
	ContentType   string
	ContentMD5B64 string
	UserMetadata  map[string]string
}

// CopyOptions carries the conditions and replacement metadata for a copy.
type CopyOptions struct {
	CopySourceIfMatch           string
	CopySourceIfNoneMatch       string
	CopySourceIfModifiedSince   time.Time
	CopySourceIfUnmodifiedSince time.Time
	ContentType                 string
	MetadataDirective           string
	UserMetadata                map[string]string
}

// ObjectReader is a handle over an immutable object version.
type ObjectReader struct {
	Info ObjectInfo
	body io.ReadCloser
}

// Read passes through to the blob file.
func (r *ObjectReader) Read(p []byte) (int, error) { return r.body.Read(p) }

// Close releases the blob handle.
func (r *ObjectReader) Close() error { return r.body.Close() }

// Seek positions the underlying blob file for range reads.
func (r *ObjectReader) Seek(offset int64, whence int) (int64, error) {
	seeker, ok := r.body.(io.Seeker)
	if !ok {
		return 0, protocol.ErrInternal
	}
	return seeker.Seek(offset, whence)
}

// Store is the on-disk object store.
type Store struct {
	root string

	mu       sync.RWMutex
	buckets  map[string]map[string]*ObjectInfo
	uploads  map[string]*UploadState
	keyLocks keyLocks

	now func() time.Time
}

type keyLocks struct {
	mu    sync.Mutex
	locks map[string]*sync.Mutex
}

func (kl *keyLocks) acquire(bucket, key string) *sync.Mutex {
	kl.mu.Lock()
	defer kl.mu.Unlock()
	id := bucket + "\x00" + key
	l, ok := kl.locks[id]
	if !ok {
		l = &sync.Mutex{}
		kl.locks[id] = l
	}
	return l
}

// Open loads metadata from disk and performs crash recovery.
func Open(ctx context.Context, root string) (*Store, error) {
	if err := os.MkdirAll(filepath.Join(root, "buckets"), 0o755); err != nil {
		return nil, err
	}
	if err := os.MkdirAll(filepath.Join(root, "blobs", "tmp"), 0o755); err != nil {
		return nil, err
	}
	s := &Store{
		root:     root,
		buckets:  map[string]map[string]*ObjectInfo{},
		uploads:  map[string]*UploadState{},
		keyLocks: keyLocks{locks: map[string]*sync.Mutex{}},
		now:      time.Now,
	}
	if err := s.load(ctx); err != nil {
		return nil, err
	}
	return s, nil
}

// Close flushes and releases resources.
func (s *Store) Close() error { return nil }

func (s *Store) bucketDir(bucket string) string {
	return filepath.Join(s.root, "buckets", hexKey(bucket))
}

func (s *Store) metaPath(bucket, key string) string {
	return filepath.Join(s.bucketDir(bucket), "objects", hexKey(key)+".json")
}

func (s *Store) blobPath(blobID string) string {
	return filepath.Join(s.root, "blobs", blobID[:2], blobID)
}

func hexKey(k string) string { return hex.EncodeToString([]byte(k)) }

// writeBlob streams data to a content-addressed blob, returning the blob id,
// size and MD5 etag. The blob is fsynced and hard-linked into place.
func (s *Store) writeBlob(body io.Reader) (blobID string, size int64, etag string, err error) {
	tmp, err := os.CreateTemp(filepath.Join(s.root, "blobs", "tmp"), "incoming-*")
	if err != nil {
		return "", 0, "", err
	}
	tmpName := tmp.Name()
	defer func() {
		if err != nil {
			tmp.Close()
			_ = os.Remove(tmpName)
		}
	}()

	md5sum := md5.New()
	n, err := io.Copy(io.MultiWriter(tmp, md5sum), body)
	if err != nil {
		return "", 0, "", err
	}
	if err = tmp.Sync(); err != nil {
		return "", 0, "", err
	}
	if err = tmp.Close(); err != nil {
		return "", 0, "", err
	}

	etag = hex.EncodeToString(md5sum.Sum(nil))
	blobID = etag
	dest := s.blobPath(blobID)
	if err = os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		return "", 0, "", err
	}
	if linkErr := os.Link(tmpName, dest); linkErr != nil && !os.IsExist(linkErr) {
		return "", 0, "", linkErr
	}
	if err = syncDir(filepath.Dir(dest)); err != nil {
		return "", 0, "", err
	}
	_ = os.Remove(tmpName)
	return blobID, n, etag, nil
}

// commitObject persists metadata and installs the object atomically.
func (s *Store) commitObject(info *ObjectInfo) error {
	path := s.metaPath(info.Bucket, info.Key)
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	if err := writeMetadataAtomic(path, info); err != nil {
		return err
	}
	if err := syncDir(dir); err != nil {
		return err
	}
	s.mu.Lock()
	cp := *info
	s.buckets[info.Bucket][info.Key] = &cp
	s.mu.Unlock()
	return nil
}

// CreateBucket creates a bucket.
func (s *Store) CreateBucket(ctx context.Context, bucket string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.buckets[bucket]; ok {
		return protocol.ErrBucketAlreadyExists
	}
	if err := os.MkdirAll(filepath.Join(s.bucketDir(bucket), "objects"), 0o755); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Join(s.bucketDir(bucket), "uploads"), 0o755); err != nil {
		return err
	}
	if err := syncDir(filepath.Join(s.root, "buckets")); err != nil {
		return err
	}
	s.buckets[bucket] = map[string]*ObjectInfo{}
	return nil
}

// DeleteBucket deletes an empty bucket.
func (s *Store) DeleteBucket(ctx context.Context, bucket string) error {
	s.mu.Lock()
	if _, ok := s.buckets[bucket]; !ok {
		s.mu.Unlock()
		return protocol.ErrNoSuchBucket
	}
	if len(s.buckets[bucket]) > 0 {
		s.mu.Unlock()
		return protocol.ErrBucketNotEmpty
	}
	for _, st := range s.uploads {
		if st.Bucket == bucket {
			s.mu.Unlock()
			return protocol.ErrBucketNotEmpty
		}
	}
	delete(s.buckets, bucket)
	s.mu.Unlock()

	if err := os.RemoveAll(s.bucketDir(bucket)); err != nil {
		return err
	}
	return syncDir(filepath.Join(s.root, "buckets"))
}

// BucketExists reports whether a bucket exists.
func (s *Store) BucketExists(ctx context.Context, bucket string) (bool, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	_, ok := s.buckets[bucket]
	return ok, nil
}

// ListBuckets returns all bucket names.
func (s *Store) ListBuckets(ctx context.Context) ([]string, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	names := make([]string, 0, len(s.buckets))
	for name := range s.buckets {
		names = append(names, name)
	}
	sortStrings(names)
	return names, nil
}

// PutObject writes a complete version of an object.
func (s *Store) PutObject(ctx context.Context, bucket, key string, body io.Reader, opts PutOptions) (*ObjectInfo, error) {
	s.mu.RLock()
	if _, ok := s.buckets[bucket]; !ok {
		s.mu.RUnlock()
		return nil, protocol.ErrNoSuchBucket
	}
	s.mu.RUnlock()

	unlock := s.keyLocks.acquire(bucket, key)
	unlock.Lock()
	defer unlock.Unlock()

	blobID, size, etag, err := s.writeBlob(body)
	if err != nil {
		return nil, err
	}
	if opts.ContentMD5B64 != "" {
		expected, decErr := base64.StdEncoding.DecodeString(opts.ContentMD5B64)
		if decErr != nil || len(expected) != 16 {
			return nil, protocol.ErrInvalidDigest
		}
		if hex.EncodeToString(expected) != etag {
			return nil, protocol.ErrBadDigest
		}
	}
	info := &ObjectInfo{
		Bucket:       bucket,
		Key:          key,
		Size:         size,
		ETag:         etag,
		ContentType:  opts.ContentType,
		LastModified: s.now().UTC(),
		UserMetadata: cloneMetadata(opts.UserMetadata),
		BlobID:       blobID,
	}
	if err := s.commitObject(info); err != nil {
		return nil, err
	}

	s.garbageCollectBlobs()
	cp := *info
	return &cp, nil
}

// snapshotInfo returns an immutable copy of committed metadata.
func (s *Store) snapshotInfo(bucket, key string) (*ObjectInfo, error) {
	s.mu.RLock()
	objs, ok := s.buckets[bucket]
	if !ok {
		s.mu.RUnlock()
		return nil, protocol.ErrNoSuchBucket
	}
	info, ok := objs[key]
	s.mu.RUnlock()
	if !ok {
		return nil, protocol.ErrNoSuchKey
	}
	cp := *info
	return &cp, nil
}

// GetObject opens an immutable committed version of an object.
func (s *Store) GetObject(ctx context.Context, bucket, key string) (*ObjectReader, error) {
	info, err := s.snapshotInfo(bucket, key)
	if err != nil {
		return nil, err
	}
	f, err := os.Open(s.blobPath(info.BlobID))
	if err != nil {
		return nil, err
	}
	return &ObjectReader{Info: *info, body: f}, nil
}

// HeadObject returns metadata of a committed object.
func (s *Store) HeadObject(ctx context.Context, bucket, key string) (*ObjectInfo, error) {
	return s.snapshotInfo(bucket, key)
}

// DeleteObject removes an object.
func (s *Store) DeleteObject(ctx context.Context, bucket, key string) error {
	s.mu.RLock()
	if _, ok := s.buckets[bucket]; !ok {
		s.mu.RUnlock()
		return protocol.ErrNoSuchBucket
	}
	s.mu.RUnlock()

	unlock := s.keyLocks.acquire(bucket, key)
	unlock.Lock()
	defer unlock.Unlock()

	path := s.metaPath(bucket, key)
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return err
	}
	if err := syncDir(filepath.Dir(path)); err != nil {
		return err
	}
	s.mu.Lock()
	delete(s.buckets[bucket], key)
	s.mu.Unlock()

	s.garbageCollectBlobs()
	return nil
}

// CopyObject performs a conditional, atomic server-side copy.
func (s *Store) CopyObject(ctx context.Context, bucket, key, srcBucket, srcKey string, opts CopyOptions) (*ObjectInfo, error) {
	src, err := s.snapshotInfo(srcBucket, srcKey)
	if err != nil {
		return nil, err
	}
	if err = evaluateSourceConditions(src, opts); err != nil {
		return nil, err
	}

	s.mu.RLock()
	if _, ok := s.buckets[bucket]; !ok {
		s.mu.RUnlock()
		return nil, protocol.ErrNoSuchBucket
	}
	s.mu.RUnlock()

	unlock := s.keyLocks.acquire(bucket, key)
	unlock.Lock()
	defer unlock.Unlock()

	info := &ObjectInfo{
		Bucket:       bucket,
		Key:          key,
		Size:         src.Size,
		ETag:         src.ETag,
		LastModified: s.now().UTC(),
		BlobID:       src.BlobID,
	}
	switch opts.MetadataDirective {
	case "REPLACE":
		info.ContentType = opts.ContentType
		info.UserMetadata = cloneMetadata(opts.UserMetadata)
	default:
		info.ContentType = src.ContentType
		info.UserMetadata = cloneMetadata(src.UserMetadata)
	}
	if err := s.commitObject(info); err != nil {
		return nil, err
	}
	cp := *info
	return &cp, nil
}
