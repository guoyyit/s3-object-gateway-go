package storage

import (
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"sort"
	"time"

	"github.com/guoyyit/s3-object-gateway-go/internal/protocol"
)

const (
	minPartSize = 5 * 1024 * 1024
	maxPartNum  = 10000
)

// UploadState is the durable state of one multipart upload.
type UploadState struct {
	Bucket       string            `json:"bucket"`
	Key          string            `json:"key"`
	UploadID     string            `json:"uploadId"`
	ContentType  string            `json:"contentType,omitempty"`
	UserMetadata map[string]string `json:"userMetadata,omitempty"`
	Initiated    time.Time         `json:"initiated"`
	Parts        []PartRecord      `json:"parts"`
}

// PartRecord is one stored part.
type PartRecord struct {
	PartNumber int    `json:"partNumber"`
	ETag       string `json:"etag"`
	Size       int64  `json:"size"`
	BlobID     string `json:"blobId"`
}

// UploadInfo describes an in-progress multipart upload.
type UploadInfo struct {
	Bucket       string
	Key          string
	UploadID     string
	ContentType  string
	UserMetadata map[string]string
	Initiated    time.Time
}

// PartInfo describes one uploaded part.
type PartInfo struct {
	PartNumber int
	ETag       string
	Size       int64
}

func (s *Store) uploadDir(bucket, uploadID string) string {
	return filepath.Join(s.bucketDir(bucket), "uploads", uploadID)
}

func (s *Store) uploadStatePath(bucket, uploadID string) string {
	return filepath.Join(s.uploadDir(bucket, uploadID), "upload.json")
}

func uploadKey(bucket, uploadID string) string { return bucket + "\x00" + uploadID }

func copyUploadState(in *UploadState) *UploadState {
	cp := *in
	cp.UserMetadata = cloneMetadata(in.UserMetadata)
	cp.Parts = append([]PartRecord(nil), in.Parts...)
	return &cp
}

func (s *Store) saveUploadState(st *UploadState) error {
	dir := s.uploadDir(st.Bucket, st.UploadID)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(st, "", "  ")
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".state-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmpName, s.uploadStatePath(st.Bucket, st.UploadID)); err != nil {
		return err
	}
	return syncDir(dir)
}

// CreateMultipartUpload starts a new multipart upload.
func (s *Store) CreateMultipartUpload(ctx context.Context, bucket, key string, opts PutOptions) (*UploadInfo, error) {
	s.mu.RLock()
	if _, ok := s.buckets[bucket]; !ok {
		s.mu.RUnlock()
		return nil, protocol.ErrNoSuchBucket
	}
	s.mu.RUnlock()

	state := &UploadState{
		Bucket:       bucket,
		Key:          key,
		UploadID:     newUploadID(),
		ContentType:  opts.ContentType,
		UserMetadata: cloneMetadata(opts.UserMetadata),
		Initiated:    s.now().UTC(),
	}
	if err := s.saveUploadState(state); err != nil {
		return nil, err
	}
	s.mu.Lock()
	s.uploads[uploadKey(bucket, state.UploadID)] = state
	s.mu.Unlock()

	return &UploadInfo{
		Bucket:       state.Bucket,
		Key:          state.Key,
		UploadID:     state.UploadID,
		ContentType:  state.ContentType,
		UserMetadata: cloneMetadata(state.UserMetadata),
		Initiated:    state.Initiated,
	}, nil
}

func (s *Store) getUpload(bucket, uploadID string) (*UploadState, error) {
	s.mu.RLock()
	state, ok := s.uploads[uploadKey(bucket, uploadID)]
	s.mu.RUnlock()
	if !ok {
		return nil, protocol.ErrNoSuchUpload
	}
	return state, nil
}

// UploadPart stores one part of an upload.
func (s *Store) UploadPart(ctx context.Context, bucket, key, uploadID string, partNumber int, body io.Reader) (*PartInfo, error) {
	if partNumber < 1 || partNumber > maxPartNum {
		return nil, protocol.WithMessage(protocol.ErrInvalidArgument, "Part number must be between 1 and 10000")
	}
	state, err := s.getUpload(bucket, uploadID)
	if err != nil {
		return nil, err
	}
	if state.Key != key {
		return nil, protocol.ErrNoSuchUpload
	}

	blobID, size, etag, err := s.writeBlob(body)
	if err != nil {
		return nil, err
	}

	s.mu.Lock()
	replaced := false
	for i, p := range state.Parts {
		if p.PartNumber == partNumber {
			state.Parts[i] = PartRecord{PartNumber: partNumber, ETag: etag, Size: size, BlobID: blobID}
			replaced = true
			break
		}
	}
	if !replaced {
		state.Parts = append(state.Parts, PartRecord{PartNumber: partNumber, ETag: etag, Size: size, BlobID: blobID})
	}
	stateCopy := copyUploadState(state)
	s.mu.Unlock()

	if err := s.saveUploadState(stateCopy); err != nil {
		return nil, err
	}
	s.garbageCollectBlobs()
	return &PartInfo{PartNumber: partNumber, ETag: etag, Size: size}, nil
}

// CompleteMultipartUpload atomically assembles the final object. clientETags
// maps part numbers to the ETags the client received at upload time.
func (s *Store) CompleteMultipartUpload(ctx context.Context, bucket, key, uploadID string, orderedParts []int, clientETags map[int]string) (*ObjectInfo, error) {
	state, err := s.getUpload(bucket, uploadID)
	if err != nil {
		return nil, err
	}
	if state.Key != key {
		return nil, protocol.ErrNoSuchUpload
	}

	s.mu.Lock()
	byNumber := map[int]PartRecord{}
	for _, p := range state.Parts {
		byNumber[p.PartNumber] = p
	}
	selected := make([]PartRecord, 0, len(orderedParts))
	for i, num := range orderedParts {
		p, ok := byNumber[num]
		if !ok {
			s.mu.Unlock()
			return nil, protocol.ErrInvalidPart
		}
		if clientETags != nil {
			if given, exists := clientETags[num]; exists && given != p.ETag {
				s.mu.Unlock()
				return nil, protocol.ErrInvalidPart
			}
		}
		if i < len(orderedParts)-1 && p.Size < minPartSize {
			s.mu.Unlock()
			return nil, protocol.ErrEntityTooSmall
		}
		selected = append(selected, p)
	}
	s.mu.Unlock()
	if len(selected) == 0 {
		return nil, protocol.ErrInvalidPart
	}

	unlock := s.keyLocks.acquire(bucket, key)
	unlock.Lock()
	defer unlock.Unlock()

	assembledID, size, assembledETag, err := s.assembleParts(selected)
	if err != nil {
		return nil, err
	}
	info := &ObjectInfo{
		Bucket:       bucket,
		Key:          key,
		Size:         size,
		ETag:         assembledETag,
		ContentType:  state.ContentType,
		LastModified: s.now().UTC(),
		UserMetadata: cloneMetadata(state.UserMetadata),
		BlobID:       assembledID,
	}
	if err := s.commitObject(info); err != nil {
		return nil, err
	}

	s.removeUploadDir(state)
	s.mu.Lock()
	delete(s.uploads, uploadKey(bucket, uploadID))
	s.mu.Unlock()
	s.garbageCollectBlobs()
	cp := *info
	return &cp, nil
}

// AbortMultipartUpload atomically discards an upload and its parts.
func (s *Store) AbortMultipartUpload(ctx context.Context, bucket, key, uploadID string) error {
	state, err := s.getUpload(bucket, uploadID)
	if err != nil {
		return err
	}
	if state.Key != key {
		return protocol.ErrNoSuchUpload
	}
	s.removeUploadDir(state)
	s.mu.Lock()
	delete(s.uploads, uploadKey(bucket, uploadID))
	s.mu.Unlock()
	s.garbageCollectBlobs()
	return nil
}

func (s *Store) removeUploadDir(state *UploadState) {
	dir := s.uploadDir(state.Bucket, state.UploadID)
	_ = os.RemoveAll(dir)
	_ = syncDir(filepath.Join(s.bucketDir(state.Bucket), "uploads"))
}

// ListMultipartUploads lists in-progress uploads for a bucket.
func (s *Store) ListMultipartUploads(ctx context.Context, bucket string) ([]UploadInfo, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if _, ok := s.buckets[bucket]; !ok {
		return nil, protocol.ErrNoSuchBucket
	}
	out := make([]UploadInfo, 0)
	for _, st := range s.uploads {
		if st.Bucket != bucket {
			continue
		}
		out = append(out, UploadInfo{
			Bucket:       st.Bucket,
			Key:          st.Key,
			UploadID:     st.UploadID,
			ContentType:  st.ContentType,
			UserMetadata: cloneMetadata(st.UserMetadata),
			Initiated:    st.Initiated,
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Key < out[j].Key })
	return out, nil
}

// ListParts lists the parts of one upload.
func (s *Store) ListParts(ctx context.Context, bucket, key, uploadID string) ([]PartInfo, error) {
	state, err := s.getUpload(bucket, uploadID)
	if err != nil {
		return nil, err
	}
	if state.Key != key {
		return nil, protocol.ErrNoSuchUpload
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]PartInfo, 0, len(state.Parts))
	for _, p := range state.Parts {
		out = append(out, PartInfo{PartNumber: p.PartNumber, ETag: p.ETag, Size: p.Size})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].PartNumber < out[j].PartNumber })
	return out, nil
}
