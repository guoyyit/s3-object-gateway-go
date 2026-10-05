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
	"strings"
	"time"
)

// ObjectReader is a readable object body plus its metadata.
type ObjectReader struct {
	Meta ObjectMeta
	Body io.ReadCloser
}

// ListResult is one page of a bucket listing.
type ListResult struct {
	Contents       []ObjectMeta
	CommonPrefixes []string
	NextMarker     string
	IsTruncated    bool
}

const defaultMaxKeys = 1000

func newVersionID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return fmt.Sprintf("%032x", time.Now().UnixNano())
	}
	return hex.EncodeToString(b[:])
}

func buildStoredMeta(key, version, dataFile string, size int64, etag string, in ObjectMeta) storedMeta {
	return storedMeta{
		Key:                key,
		VersionID:          version,
		DataFile:           dataFile,
		Size:               size,
		ContentType:        in.ContentType,
		ContentEncoding:    in.ContentEncoding,
		ContentLanguage:    in.ContentLanguage,
		ContentDisposition: in.ContentDisposition,
		CacheControl:       in.CacheControl,
		ETag:               etag,
		LastModified:       time.Now().UTC(),
		Expires:            in.Expires,
		StorageClass:       in.StorageClass,
		Metadata:           in.Metadata,
	}
}

// PutObject creates or replaces an object. The body is fully written and
// fsynced to a versioned, immutable blob before the manifest is atomically
// updated, so readers never observe a partial write.
func (s *Store) PutObject(ctx context.Context, key string, r io.Reader, meta ObjectMeta) (*ObjectMeta, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	version := newVersionID()
	dataName := version + ".dat"
	dataPath := filepath.Join(s.dataDir, dataName)

	tmp, err := os.CreateTemp(s.tmpDir, "put-*")
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
	if err := os.Link(tmpName, dataPath); err != nil {
		os.Remove(tmpName)
		return nil, err
	}
	os.Remove(tmpName)

	etag := `"` + hex.EncodeToString(h.Sum(nil)) + `"`
	rec := buildStoredMeta(key, version, dataName, n, etag, meta)

	err = s.withManifest(func(m *manifest) error {
		m.Objects[key] = rec
		return nil
	})
	if err != nil {
		os.Remove(dataPath)
		return nil, err
	}
	out := rec.toObjectMeta()
	return &out, nil
}

// HeadObject returns metadata for an object without reading its body.
func (s *Store) HeadObject(ctx context.Context, key string) (*ObjectMeta, error) {
	m, err := s.snapshotManifest()
	if err != nil {
		return nil, err
	}
	rec, ok := m.Objects[key]
	if !ok {
		return nil, ErrNotFound
	}
	out := rec.toObjectMeta()
	return &out, nil
}

// GetObject opens an object, optionally restricted to the given byte range.
func (s *Store) GetObject(ctx context.Context, key string, rng *Range) (*ObjectReader, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	m, err := s.snapshotManifest()
	if err != nil {
		return nil, err
	}
	rec, ok := m.Objects[key]
	if !ok {
		return nil, ErrNotFound
	}

	f, err := os.Open(filepath.Join(s.dataDir, rec.DataFile))
	if err != nil {
		if os.IsNotExist(err) {
			return nil, ErrNotFound
		}
		return nil, err
	}

	meta := rec.toObjectMeta()
	if rng == nil {
		return &ObjectReader{Meta: meta, Body: f}, nil
	}
	if rng.Start < 0 || rng.End < rng.Start || rng.Start >= rec.Size {
		f.Close()
		return nil, ErrInvalidRange
	}
	if rng.End >= rec.Size {
		rng.End = rec.Size - 1
	}
	length := rng.End - rng.Start + 1
	if _, err := f.Seek(rng.Start, io.SeekStart); err != nil {
		f.Close()
		return nil, err
	}
	meta.Size = length
	return &ObjectReader{
		Meta: meta,
		Body: &fileSection{f: f, length: length},
	}, nil
}

// DeleteObject removes an object. Deleting a missing key is a no-op.
func (s *Store) DeleteObject(ctx context.Context, key string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	return s.withManifest(func(m *manifest) error {
		delete(m.Objects, key)
		return nil
	})
}

// CopyObject duplicates an object atomically. Blobs are immutable, so the
// destination is published as a fresh version hard-linked to the source blob.
func (s *Store) CopyObject(ctx context.Context, src, dst string) (*ObjectMeta, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	m, err := s.snapshotManifest()
	if err != nil {
		return nil, err
	}
	srcRec, ok := m.Objects[src]
	if !ok {
		return nil, ErrNotFound
	}

	version := newVersionID()
	dstDataName := version + ".dat"
	if err := os.Link(filepath.Join(s.dataDir, srcRec.DataFile), filepath.Join(s.dataDir, dstDataName)); err != nil {
		return nil, err
	}

	dstRec := srcRec
	dstRec.Key = dst
	dstRec.VersionID = version
	dstRec.DataFile = dstDataName
	dstRec.LastModified = time.Now().UTC()

	if err := s.withManifest(func(mm *manifest) error {
		mm.Objects[dst] = dstRec
		return nil
	}); err != nil {
		os.Remove(filepath.Join(s.dataDir, dstDataName))
		return nil, err
	}
	out := dstRec.toObjectMeta()
	return &out, nil
}

// ParseRange parses an RFC 7233 byte range relative to an object size.
// An empty header means "the whole object" and returns (nil, nil).
func ParseRange(header string, size int64) (*Range, error) {
	header = strings.TrimSpace(header)
	if header == "" {
		return nil, nil
	}
	if !strings.HasPrefix(header, "bytes=") {
		return nil, ErrInvalidRange
	}
	specs := strings.Split(strings.TrimPrefix(header, "bytes="), ",")
	if len(specs) != 1 {
		return nil, ErrInvalidRange
	}
	spec := strings.TrimSpace(specs[0])
	startS, endS, ok := strings.Cut(spec, "-")
	if !ok {
		return nil, ErrInvalidRange
	}
	switch {
	case startS == "":
		n, err := parseInt64(endS)
		if err != nil || n <= 0 {
			return nil, ErrInvalidRange
		}
		if size == 0 || n > size {
			if size == 0 {
				return nil, ErrInvalidRange
			}
			n = size
		}
		return &Range{Start: size - n, End: size - 1}, nil
	case endS == "":
		n, err := parseInt64(startS)
		if err != nil || n < 0 || n >= size {
			return nil, ErrInvalidRange
		}
		return &Range{Start: n, End: size - 1}, nil
	default:
		start, err := parseInt64(startS)
		if err != nil || start < 0 {
			return nil, ErrInvalidRange
		}
		end, err := parseInt64(endS)
		if err != nil || end < start || start >= size {
			return nil, ErrInvalidRange
		}
		if end >= size {
			end = size - 1
		}
		return &Range{Start: start, End: end}, nil
	}
}

func parseInt64(s string) (int64, error) {
	var n int64
	_, err := fmt.Sscanf(s, "%d", &n)
	return n, err
}

// ListObjects returns one consistent snapshot page of objects. The page is
// computed from a single manifest snapshot, so concurrent writes or deletes
// during paging never duplicate or drop keys, and NextMarker stays valid.
func (s *Store) ListObjects(ctx context.Context, prefix, delimiter, marker string, maxKeys int) (*ListResult, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if maxKeys <= 0 {
		maxKeys = defaultMaxKeys
	}
	m, err := s.snapshotManifest()
	if err != nil {
		return nil, err
	}

	keys := make([]string, 0, len(m.Objects))
	for k := range m.Objects {
		if strings.HasPrefix(k, prefix) {
			keys = append(keys, k)
		}
	}
	sort.Strings(keys)

	res := &ListResult{}
	prefixSeen := map[string]bool{}
	type row struct {
		key    string
		common string
	}
	var ordered []row
	for _, k := range keys {
		var common string
		if delimiter != "" {
			rest := strings.TrimPrefix(k, prefix)
			if idx := strings.Index(rest, delimiter); idx >= 0 {
				common = prefix + rest[:idx+len(delimiter)]
			}
		}
		if common != "" {
			if prefixSeen[common] || common == marker {
				continue
			}
			prefixSeen[common] = true
		}
		// For plain keys the marker is an exclusive lexicographic boundary.
		if common == "" && k <= marker {
			continue
		}
		ordered = append(ordered, row{key: k, common: common})
	}

	sort.Slice(ordered, func(i, j int) bool {
		label := func(r row) string {
			if r.common != "" {
				return r.common
			}
			return r.key
		}
		return label(ordered[i]) < label(ordered[j])
	})

	if len(ordered) > maxKeys {
		res.IsTruncated = true
		ordered = ordered[:maxKeys]
		last := ordered[maxKeys-1]
		if last.common != "" {
			res.NextMarker = last.common
		} else {
			res.NextMarker = last.key
		}
	}

	for _, rw := range ordered {
		if rw.common != "" {
			res.CommonPrefixes = append(res.CommonPrefixes, rw.common)
		} else {
			rec := m.Objects[rw.key]
			res.Contents = append(res.Contents, rec.toObjectMeta())
		}
	}
	return res, nil
}

// fileSection limits reads to a byte range and closes the underlying file.
type fileSection struct {
	f      *os.File
	length int64
	read   int64
}

func (n *fileSection) Read(p []byte) (int, error) {
	if n.read >= n.length {
		return 0, io.EOF
	}
	if int64(len(p)) > n.length-n.read {
		p = p[:n.length-n.read]
	}
	nn, err := n.f.Read(p)
	n.read += int64(nn)
	if err == nil && n.read >= n.length {
		err = io.EOF
	}
	return nn, err
}

func (n *fileSection) Close() error { return n.f.Close() }
