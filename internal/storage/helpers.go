package storage

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"

	"github.com/guoyyit/s3-object-gateway-go/internal/protocol"
)

// metadataFile is the on-disk JSON representation of ObjectInfo.
type metadataFile struct {
	Bucket       string            `json:"bucket"`
	Key          string            `json:"key"`
	Size         int64             `json:"size"`
	ETag         string            `json:"etag"`
	ContentType  string            `json:"contentType,omitempty"`
	LastModified string            `json:"lastModified"`
	UserMetadata map[string]string `json:"userMetadata,omitempty"`
	BlobID       string            `json:"blobId"`
}

func writeMetadataAtomic(path string, info *ObjectInfo) error {
	data, err := json.MarshalIndent(metadataFile{
		Bucket:       info.Bucket,
		Key:          info.Key,
		Size:         info.Size,
		ETag:         info.ETag,
		ContentType:  info.ContentType,
		LastModified: info.LastModified.UTC().Format("2006-01-02T15:04:05.000Z"),
		UserMetadata: info.UserMetadata,
		BlobID:       info.BlobID,
	}, "", "  ")
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".meta-*")
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
	return os.Rename(tmpName, path)
}

func syncDir(dir string) error {
	d, err := os.Open(dir)
	if err != nil {
		return err
	}
	err = d.Sync()
	_ = d.Close()
	return err
}

func cloneMetadata(in map[string]string) map[string]string {
	if len(in) == 0 {
		return map[string]string{}
	}
	out := make(map[string]string, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}

func sortStrings(s []string) { sort.Strings(s) }

// evaluateSourceConditions applies x-amz-copy-source-* conditions.
func evaluateSourceConditions(src *ObjectInfo, opts CopyOptions) error {
	if opts.CopySourceIfMatch != "" && !etagEqual(src.ETag, opts.CopySourceIfMatch) {
		return protocol.ErrPreconditionFailed
	}
	if opts.CopySourceIfNoneMatch != "" && etagEqual(src.ETag, opts.CopySourceIfNoneMatch) {
		return protocol.ErrPreconditionFailed
	}
	if !opts.CopySourceIfUnmodifiedSince.IsZero() &&
		src.LastModified.After(opts.CopySourceIfUnmodifiedSince) {
		return protocol.ErrPreconditionFailed
	}
	if !opts.CopySourceIfModifiedSince.IsZero() &&
		!src.LastModified.After(opts.CopySourceIfModifiedSince) {
		return protocol.ErrNotModified
	}
	return nil
}

func etagEqual(etag, header string) bool {
	if len(header) >= 2 && header[0] == '"' && header[len(header)-1] == '"' {
		header = header[1 : len(header)-1]
	}
	return etag == header
}

// liveBlobIDs computes the set of blob ids referenced by objects and parts.
func (s *Store) liveBlobIDs() map[string]struct{} {
	live := map[string]struct{}{}
	s.mu.RLock()
	for _, objs := range s.buckets {
		for _, info := range objs {
			live[info.BlobID] = struct{}{}
		}
	}
	for _, up := range s.uploads {
		for _, p := range up.Parts {
			live[p.BlobID] = struct{}{}
		}
	}
	s.mu.RUnlock()
	return live
}

func (s *Store) garbageCollectBlobs() {
	live := s.liveBlobIDs()
	root := filepath.Join(s.root, "blobs")
	_ = filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		name := d.Name()
		if filepath.Dir(path) == filepath.Join(root, "tmp") {
			_ = os.Remove(path)
			return nil
		}
		if len(name) != 32 {
			return nil
		}
		if _, ok := live[name]; !ok {
			_ = os.Remove(path)
		}
		return nil
	})
}
