// Package storage implements the durable local object store.
package storage

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"time"
)

var (
	// ErrNotFound indicates that the object or upload does not exist.
	ErrNotFound = errors.New("not found")
	// ErrInvalidRange indicates an unsatisfiable or malformed byte range.
	ErrInvalidRange = errors.New("invalid range")
)

// ObjectMeta holds an object's metadata and common content headers.
type ObjectMeta struct {
	Key                string
	Size               int64
	ContentType        string
	ContentEncoding    string
	ContentLanguage    string
	ContentDisposition string
	CacheControl       string
	ETag               string
	LastModified       time.Time
	Expires            time.Time
	StorageClass       string
	Metadata           map[string]string
}

// Range is a closed byte interval [Start, End].
type Range struct {
	Start int64
	End   int64
}

// Store is the durable object store.
type Store struct {
	root string
	mu   sync.RWMutex

	// data: committed blobs; uploads: multipart state; tmp: scratch blobs.
	dataDir    string
	uploadDir  string
	tmpDir     string
	manifestMu sync.Mutex
}

// Open opens (or initializes) a store rooted at the given directory.
// It recovers state from disk and removes unreferenced scratch files.
func Open(root string) (*Store, error) {
	s := &Store{root: root}
	s.dataDir = filepath.Join(root, "data")
	s.uploadDir = filepath.Join(root, "uploads")
	s.tmpDir = filepath.Join(root, "tmp")
	for _, d := range []string{s.dataDir, s.uploadDir, s.tmpDir} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			return nil, err
		}
	}
	if err := s.recover(); err != nil {
		return nil, err
	}
	return s, nil
}

// Close releases all resources held by the store.
func (s *Store) Close() error {
	return nil
}

func (s *Store) manifestPath() string {
	return filepath.Join(s.root, "manifest.json")
}

func (s *Store) loadManifest() (*manifest, error) {
	m := &manifest{Objects: map[string]storedMeta{}}
	if err := readJSONFile(s.manifestPath(), m); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return m, nil
		}
		return nil, err
	}
	if m.Objects == nil {
		m.Objects = map[string]storedMeta{}
	}
	return m, nil
}

// withManifest loads the manifest and persists it after fn succeeds.
// The manifest lock serializes all mutating operations so concurrent writes
// converge to one complete committed version.
func (s *Store) withManifest(fn func(m *manifest) error) error {
	s.manifestMu.Lock()
	defer s.manifestMu.Unlock()
	m, err := s.loadManifest()
	if err != nil {
		return err
	}
	if err := fn(m); err != nil {
		return err
	}
	return writeJSONFile(s.manifestPath(), m)
}

// snapshotManifest returns a deep-enough copy of the manifest for reads.
func (s *Store) snapshotManifest() (*manifest, error) {
	s.manifestMu.Lock()
	defer s.manifestMu.Unlock()
	return s.loadManifest()
}

// SweepAbandonedUploads aborts multipart uploads initiated before cutoff and
// reclaims their parts. Safe to call concurrently.
func (s *Store) SweepAbandonedUploads(_ context.Context, cutoff time.Time) error {
	return s.sweep(cutoff)
}
