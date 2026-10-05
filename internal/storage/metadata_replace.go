package storage

import "context"

// ReplaceMetadata publishes a new version record for key that points at the
// same immutable blob but carries the provided system/user metadata. The
// object content (and therefore ETag/size) is unchanged.
func (s *Store) ReplaceMetadata(ctx context.Context, key string, meta ObjectMeta) (*ObjectMeta, error) {
	s.manifestMu.Lock()
	defer s.manifestMu.Unlock()

	m, err := s.loadManifest()
	if err != nil {
		return nil, err
	}
	rec, ok := m.Objects[key]
	if !ok {
		return nil, ErrNotFound
	}
	rec.ContentType = meta.ContentType
	rec.ContentEncoding = meta.ContentEncoding
	rec.ContentLanguage = meta.ContentLanguage
	rec.ContentDisposition = meta.ContentDisposition
	rec.CacheControl = meta.CacheControl
	rec.Expires = meta.Expires
	rec.Metadata = meta.Metadata
	m.Objects[key] = rec
	if err := writeJSONFile(s.manifestPath(), m); err != nil {
		return nil, err
	}
	out := rec.toObjectMeta()
	return &out, nil
}
