package storage

import (
	"os"
	"path/filepath"
	"time"
)

// recover reconciles on-disk state after a (possibly unclean) shutdown:
//   - removes staged-but-unpublished files from tmp/,
//   - removes data blobs no longer referenced by the manifest,
//   - removes part files no longer referenced by an upload record,
//   - removes upload directories whose JSON record is missing or unreadable.
//
// In-progress multipart uploads with valid records are kept so they can be
// continued or explicitly aborted/completed.
func (s *Store) recover() error {
	// 1. tmp/ holds only scratch files that never reached a publish point.
	if err := removeContents(s.tmpDir); err != nil {
		return err
	}

	m, err := s.loadManifest()
	if err != nil {
		return err
	}
	liveBlobs := map[string]bool{}
	for _, rec := range m.Objects {
		liveBlobs[rec.DataFile] = true
	}

	// 2. Collect part files referenced by live uploads.
	liveParts := map[string]bool{}
	uploadDirs, err := listDirs(s.uploadDir)
	if err != nil {
		return err
	}
	liveUploadDirs := map[string]bool{}
	for _, id := range uploadDirs {
		dir := filepath.Join(s.uploadDir, id)
		up, err := s.readUpload(id)
		if err != nil {
			// Corrupt or half-written record: drop the whole directory so
			// it cannot leak into later writes.
			os.RemoveAll(dir)
			continue
		}
		liveUploadDirs[id] = true
		for _, p := range up.Parts {
			liveParts[p.File] = true
		}
	}

	// 3. Remove unreferenced data blobs. Blobs are removed only during
	// recovery (no readers exist yet) to avoid racing an open file.
	if err := removeUnreferenced(s.dataDir, liveBlobs, ".dat"); err != nil {
		return err
	}

	// 4. Remove orphaned part files across all upload directories.
	for _, id := range uploadDirs {
		if !liveUploadDirs[id] {
			continue
		}
		dir := filepath.Join(s.uploadDir, id)
		if err := removeUnreferenced(dir, liveParts, ".part"); err != nil {
			return err
		}
	}
	return nil
}

// sweep aborts multipart uploads initiated before cutoff and reclaims their
// parts. Used to reclaim uploads abandoned by timeout or client crash.
func (s *Store) sweep(cutoff time.Time) error {
	uploadDirs, err := listDirs(s.uploadDir)
	if err != nil {
		return err
	}
	for _, id := range uploadDirs {
		up, err := s.readUpload(id)
		if err != nil {
			os.RemoveAll(filepath.Join(s.uploadDir, id))
			continue
		}
		if up.Initiated.Before(cutoff) {
			_ = os.RemoveAll(filepath.Join(s.uploadDir, id))
		}
	}
	return nil
}

func listDirs(dir string) ([]string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var out []string
	for _, e := range entries {
		if e.IsDir() {
			out = append(out, e.Name())
		}
	}
	return out, nil
}

func removeContents(dir string) error {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	for _, e := range entries {
		if err := os.RemoveAll(filepath.Join(dir, e.Name())); err != nil {
			return err
		}
	}
	return nil
}

func removeUnreferenced(dir string, live map[string]bool, suffix string) error {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		if suffix != "" && filepath.Ext(name) != suffix {
			continue
		}
		if !live[name] {
			if err := os.Remove(filepath.Join(dir, name)); err != nil && !os.IsNotExist(err) {
				return err
			}
		}
	}
	return nil
}
