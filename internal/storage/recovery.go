package storage

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// load rebuilds the in-memory index from committed metadata files and
// durable multipart-upload state. It also removes stale temp blobs.
func (s *Store) load(ctx context.Context) error {
	entries, err := os.ReadDir(filepath.Join(s.root, "buckets"))
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		bucket, derr := hex.DecodeString(entry.Name())
		if derr != nil {
			continue
		}
		bucketName := string(bucket)
		s.buckets[bucketName] = map[string]*ObjectInfo{}

		objDir := filepath.Join(s.bucketDir(bucketName), "objects")
		objEntries, _ := os.ReadDir(objDir)
		for _, oe := range objEntries {
			if oe.IsDir() || !strings.HasSuffix(oe.Name(), ".json") {
				continue
			}
			keyBytes, kerr := hex.DecodeString(strings.TrimSuffix(oe.Name(), ".json"))
			if kerr != nil {
				continue
			}
			data, rerr := os.ReadFile(filepath.Join(objDir, oe.Name()))
			if rerr != nil {
				continue
			}
			var mf metadataFile
			if jerr := json.Unmarshal(data, &mf); jerr != nil {
				continue
			}
			mod, perr := time.Parse("2006-01-02T15:04:05.000Z", mf.LastModified)
			if perr != nil {
				mod = s.now()
			}
			info := &ObjectInfo{
				Bucket:       bucketName,
				Key:          string(keyBytes),
				Size:         mf.Size,
				ETag:         mf.ETag,
				ContentType:  mf.ContentType,
				LastModified: mod,
				UserMetadata: cloneMetadata(mf.UserMetadata),
				BlobID:       mf.BlobID,
			}
			if _, statErr := os.Stat(s.blobPath(info.BlobID)); statErr != nil {
				// Corrupted/intermediate metadata is never exposed to readers.
				_ = os.Remove(filepath.Join(objDir, oe.Name()))
				continue
			}
			s.buckets[bucketName][info.Key] = info
		}

		uploadsRoot := filepath.Join(s.bucketDir(bucketName), "uploads")
		uploadEntries, _ := os.ReadDir(uploadsRoot)
		for _, ue := range uploadEntries {
			if !ue.IsDir() {
				continue
			}
			data, rerr := os.ReadFile(filepath.Join(uploadsRoot, ue.Name(), "upload.json"))
			if rerr != nil {
				_ = os.RemoveAll(filepath.Join(uploadsRoot, ue.Name()))
				continue
			}
			var st UploadState
			if jerr := json.Unmarshal(data, &st); jerr != nil {
				_ = os.RemoveAll(filepath.Join(uploadsRoot, ue.Name()))
				continue
			}
			st.Bucket = bucketName
			s.uploads[uploadKey(bucketName, st.UploadID)] = &st
		}
	}

	// Stale temp blobs from a crash cannot be referenced; remove them.
	s.garbageCollectBlobs()
	return nil
}

// SweepAbandoned aborts multipart uploads older than maxAge and
// garbage-collects unreferenced blob files. Uploads remain resumable until
// they exceed the age limit.
func (s *Store) SweepAbandoned(ctx context.Context, maxAge time.Duration) error {
	s.mu.Lock()
	stale := make([]*UploadState, 0)
	cutoff := s.now().Add(-maxAge)
	for _, st := range s.uploads {
		if st.Initiated.Before(cutoff) {
			stale = append(stale, st)
		}
	}
	s.mu.Unlock()
	for _, st := range stale {
		_ = s.AbortMultipartUpload(ctx, st.Bucket, st.Key, st.UploadID)
	}
	s.garbageCollectBlobs()
	return nil
}
