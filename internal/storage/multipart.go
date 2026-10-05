package storage

import (
	"context"
	"crypto/md5"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"time"
)

// PartInfo describes an uploaded part.
type PartInfo struct {
	PartNumber int
	ETag       string
	Size       int64
}

// MultipartUpload describes an in-progress multipart upload.
type MultipartUpload struct {
	UploadID  string
	Key       string
	Initiated time.Time
}

const (
	minPartSize = 5 * 1024 * 1024
	maxPartNum  = 10000
)

func (s *Store) uploadJSONPath(id string) string {
	return filepath.Join(s.uploadDir, id, "upload.json")
}

func (s *Store) readUpload(id string) (*storedUpload, error) {
	var up storedUpload
	if err := readJSONFile(s.uploadJSONPath(id), &up); err != nil {
		return nil, err
	}
	return &up, nil
}

func (s *Store) writeUpload(up *storedUpload) error {
	return writeJSONFile(s.uploadJSONPath(up.UploadID), up)
}

// InitiateMultipart starts a multipart upload for a key.
func (s *Store) InitiateMultipart(ctx context.Context, key string, meta ObjectMeta) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	id := hex.EncodeToString(b[:])
	if err := os.MkdirAll(filepath.Join(s.uploadDir, id), 0o755); err != nil {
		return "", err
	}
	up := &storedUpload{
		UploadID:  id,
		Key:       key,
		Initiated: time.Now().UTC(),
		Meta:      buildStoredMeta(key, "", "", 0, "", meta),
	}
	if err := s.writeUpload(up); err != nil {
		os.RemoveAll(filepath.Join(s.uploadDir, id))
		return "", err
	}
	return id, nil
}

// UploadPart stores one part of a multipart upload.
func (s *Store) UploadPart(ctx context.Context, uploadID string, partNumber int, r io.Reader) (*PartInfo, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if partNumber < 1 || partNumber > maxPartNum {
		return nil, ErrInvalidPartNumber
	}

	s.manifestMu.Lock()
	defer s.manifestMu.Unlock()

	up, err := s.readUpload(uploadID)
	if err != nil {
		return nil, ErrNotFound
	}

	dir := filepath.Join(s.uploadDir, uploadID)
	tmp, err := os.CreateTemp(dir, ".part-*")
	if err != nil {
		return nil, err
	}
	tmpName := tmp.Name()
	h := md5.New()
	n, err := io.Copy(io.MultiWriter(tmp, h), r)
	if err != nil {
		tmp.Close()
		os.Remove(tmpName)
		return nil, err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		os.Remove(tmpName)
		return nil, err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmpName)
		return nil, err
	}

	partFile := fmt.Sprintf("%05d.part", partNumber)
	finalPath := filepath.Join(dir, partFile)
	os.Remove(finalPath)
	if err := os.Rename(tmpName, finalPath); err != nil {
		os.Remove(tmpName)
		return nil, err
	}

	etag := hex.EncodeToString(h.Sum(nil))
	rec := partRec{PartNumber: partNumber, ETag: etag, Size: n, File: partFile}

	replaced := false
	for i := range up.Parts {
		if up.Parts[i].PartNumber == partNumber {
			old := up.Parts[i].File
			up.Parts[i] = rec
			replaced = true
			if old != partFile {
				os.Remove(filepath.Join(dir, old))
			}
			break
		}
	}
	if !replaced {
		up.Parts = append(up.Parts, rec)
	}
	if err := s.writeUpload(up); err != nil {
		return nil, err
	}

	return &PartInfo{PartNumber: partNumber, ETag: `"` + etag + `"`, Size: n}, nil
}

// CompleteMultipart assembles parts into a final object atomically. The
// assembled blob is fully written and fsynced before the manifest is updated,
// and the upload record is only removed after a successful commit.
func (s *Store) CompleteMultipart(ctx context.Context, uploadID string, parts []PartInfo) (*ObjectMeta, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	s.manifestMu.Lock()
	defer s.manifestMu.Unlock()

	up, err := s.readUpload(uploadID)
	if err != nil {
		return nil, ErrNotFound
	}
	byNum := map[int]partRec{}
	for _, p := range up.Parts {
		byNum[p.PartNumber] = p
	}

	ordered := make([]partRec, 0, len(parts))
	prev := 0
	etagConcat := md5.New()
	var total int64
	for i, p := range parts {
		if i > 0 && p.PartNumber <= prev {
			return nil, ErrInvalidPartOrder
		}
		prev = p.PartNumber
		rec, ok := byNum[p.PartNumber]
		if !ok {
			return nil, ErrInvalidPart
		}
		raw, derr := hex.DecodeString(unquote(p.ETag))
		if derr == nil && len(raw) == 16 && hex.EncodeToString(raw) != rec.ETag {
			return nil, ErrInvalidPart
		}
		etagConcat.Write(mustHex(rec.ETag))
		total += rec.Size
		ordered = append(ordered, rec)
	}
	if len(ordered) == 0 {
		return nil, ErrInvalidPart
	}
	// All parts except the last must meet the minimum size.
	for i := 0; i < len(ordered)-1; i++ {
		if ordered[i].Size < minPartSize {
			return nil, ErrPartTooSmall
		}
	}

	version := newVersionID()
	dataName := version + ".dat"
	dataPath := filepath.Join(s.dataDir, dataName)

	out, err := os.CreateTemp(s.tmpDir, "mp-*")
	if err != nil {
		return nil, err
	}
	tmpName := out.Name()
	for _, rec := range ordered {
		pf, oerr := os.Open(filepath.Join(s.uploadDir, uploadID, rec.File))
		if oerr != nil {
			out.Close()
			os.Remove(tmpName)
			return nil, ErrInvalidPart
		}
		_, cerr := io.Copy(out, pf)
		pf.Close()
		if cerr != nil {
			out.Close()
			os.Remove(tmpName)
			return nil, cerr
		}
	}
	if err := out.Sync(); err != nil {
		out.Close()
		os.Remove(tmpName)
		return nil, err
	}
	if err := out.Close(); err != nil {
		os.Remove(tmpName)
		return nil, err
	}
	if err := os.Link(tmpName, dataPath); err != nil {
		os.Remove(tmpName)
		return nil, err
	}
	os.Remove(tmpName)

	combined := md5.Sum(etagConcat.Sum(nil))
	etag := fmt.Sprintf("\"%s-%d\"", hex.EncodeToString(combined[:]), len(ordered))

	finalRec := up.Meta
	finalRec.VersionID = version
	finalRec.DataFile = dataName
	finalRec.Size = total
	finalRec.ETag = etag
	finalRec.LastModified = time.Now().UTC()

	m, err := s.loadManifest()
	if err != nil {
		os.Remove(dataPath)
		return nil, err
	}
	m.Objects[up.Key] = finalRec
	if err := writeJSONFile(s.manifestPath(), m); err != nil {
		os.Remove(dataPath)
		return nil, err
	}

	// Commit succeeded; remove upload state and its parts.
	os.RemoveAll(filepath.Join(s.uploadDir, uploadID))

	om := finalRec.toObjectMeta()
	return &om, nil
}

// AbortMultipart aborts an upload and removes its parts atomically with
// respect to the upload record.
func (s *Store) AbortMultipart(ctx context.Context, uploadID string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	s.manifestMu.Lock()
	defer s.manifestMu.Unlock()
	if _, err := s.readUpload(uploadID); err != nil {
		return ErrNotFound
	}
	return os.RemoveAll(filepath.Join(s.uploadDir, uploadID))
}

// ListMultipartUploads lists in-progress uploads.
func (s *Store) ListMultipartUploads(ctx context.Context, prefix, delimiter, marker string, maxUploads int) ([]MultipartUpload, string, bool, error) {
	if maxUploads <= 0 {
		maxUploads = defaultMaxKeys
	}
	dirs, err := listDirs(s.uploadDir)
	if err != nil {
		return nil, "", false, err
	}
	ups := make([]MultipartUpload, 0, len(dirs))
	for _, id := range dirs {
		up, err := s.readUpload(id)
		if err != nil {
			continue
		}
		key := up.Key
		if !startsWith(key, prefix) || key <= marker {
			continue
		}
		if delimiter != "" {
			if idx := indexDelimiter(key[len(prefix):], delimiter); idx >= 0 {
				continue
			}
		}
		ups = append(ups, MultipartUpload{UploadID: up.UploadID, Key: up.Key, Initiated: up.Initiated})
	}
	sort.Slice(ups, func(i, j int) bool { return ups[i].Key < ups[j].Key })
	truncated := len(ups) > maxUploads
	var next string
	if truncated {
		next = ups[maxUploads-1].Key
		ups = ups[:maxUploads]
	}
	return ups, next, truncated, nil
}

// ListParts lists uploaded parts for an upload in ascending part order.
func (s *Store) ListParts(ctx context.Context, uploadID string) ([]PartInfo, error) {
	up, err := s.readUpload(uploadID)
	if err != nil {
		return nil, ErrNotFound
	}
	out := make([]PartInfo, 0, len(up.Parts))
	for _, p := range up.Parts {
		out = append(out, PartInfo{PartNumber: p.PartNumber, ETag: `"` + p.ETag + `"`, Size: p.Size})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].PartNumber < out[j].PartNumber })
	return out, nil
}

func startsWith(s, prefix string) bool {
	return len(s) >= len(prefix) && s[:len(prefix)] == prefix
}

func indexDelimiter(s, d string) int {
	for i := 0; i+len(d) <= len(s); i++ {
		if s[i:i+len(d)] == d {
			return i
		}
	}
	return -1
}

func unquote(s string) string {
	if len(s) >= 2 && s[0] == '"' && s[len(s)-1] == '"' {
		return s[1 : len(s)-1]
	}
	return s
}

func mustHex(s string) []byte {
	b, err := hex.DecodeString(s)
	if err != nil {
		return nil
	}
	return b
}
