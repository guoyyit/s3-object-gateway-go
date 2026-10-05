package storage

import (
	"crypto/md5"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
)

// newUploadID returns a random hex upload identifier.
func newUploadID() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}

// assembleParts concatenates part blobs into a new content-addressed blob and
// computes the S3 multipart ETag (MD5 of concatenated part MD5s, "-N").
func (s *Store) assembleParts(parts []PartRecord) (blobID string, totalSize int64, etag string, err error) {
	tmp, err := os.CreateTemp(filepath.Join(s.root, "blobs", "tmp"), "assembled-*")
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

	overall := md5.New()
	for _, p := range parts {
		raw, derr := hex.DecodeString(p.ETag)
		if derr != nil {
			return "", 0, "", derr
		}
		overall.Write(raw)
		f, oerr := os.Open(s.blobPath(p.BlobID))
		if oerr != nil {
			return "", 0, "", oerr
		}
		n, cerr := io.Copy(tmp, f)
		f.Close()
		if cerr != nil {
			return "", 0, "", cerr
		}
		totalSize += n
	}
	multipartETag := fmt.Sprintf("%s-%d", hex.EncodeToString(overall.Sum(nil)), len(parts))

	if err = tmp.Sync(); err != nil {
		return "", 0, "", err
	}
	if _, err = tmp.Seek(0, io.SeekStart); err != nil {
		return "", 0, "", err
	}
	contentMD5 := md5.New()
	if _, err = io.Copy(contentMD5, tmp); err != nil {
		return "", 0, "", err
	}
	if err = tmp.Close(); err != nil {
		return "", 0, "", err
	}
	finalID := hex.EncodeToString(contentMD5.Sum(nil))
	dest := s.blobPath(finalID)
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
	return finalID, totalSize, multipartETag, nil
}
