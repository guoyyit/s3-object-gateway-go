package api

import (
	"net/http"
	"net/url"
	"strings"

	s3err "github.com/guoyyit/s3-object-gateway-go/internal/s3error"
	"github.com/guoyyit/s3-object-gateway-go/internal/storage"
)

// handleCopy implements PUT target with x-amz-copy-source.
func (s *Server) handleCopy(w http.ResponseWriter, r *http.Request, dstKey string) {
	srcKey, err := parseCopySource(r.Header.Get("X-Amz-Copy-Source"))
	if err != nil {
		s.respondError(w, r, s3err.New("InvalidArgument"))
		return
	}

	srcMeta, err := s.store.HeadObject(r.Context(), srcKey)
	if err != nil {
		s.respondError(w, r, s3err.New("NoSuchKey"))
		return
	}

	if code, ok := checkCopyConditions(r, srcMeta); !ok {
		s.respondError(w, r, s3err.New(code))
		return
	}

	// Metadata directive: REPLACE uses target headers, COPY (default) keeps
	// source metadata.
	var meta storage.ObjectMeta
	if strings.EqualFold(r.Header.Get("X-Amz-Metadata-Directive"), "REPLACE") {
		meta = objectMetaFromRequest(r, dstKey)
	} else {
		meta = *srcMeta
		meta.Key = dstKey
	}

	out, err := s.store.CopyObject(r.Context(), srcKey, dstKey)
	if err != nil {
		s.respondError(w, r, mapStoreError(err))
		return
	}

	// Apply chosen metadata to the freshly copied version while keeping the
	// content identity (etag/size) intact.
	if strings.EqualFold(r.Header.Get("X-Amz-Metadata-Directive"), "REPLACE") {
		if updated, uerr := s.replaceMetadata(r, dstKey, meta); uerr == nil {
			out = updated
		}
	}

	w.Header().Set("ETag", out.ETag)
	writeXML(w, http.StatusOK, copyObjectResult{
		ETag:         out.ETag,
		LastModified: out.LastModified.UTC().Format("2006-01-02T15:04:05.000Z"),
	})
}

// parseCopySource accepts "/bucket/key", "/key", "bucket/key" or a full URL.
func parseCopySource(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", s3err.New("InvalidArgument")
	}
	path := raw
	if strings.HasPrefix(raw, "http://") || strings.HasPrefix(raw, "https://") {
		u, err := url.Parse(raw)
		if err != nil {
			return "", err
		}
		path = u.Path
	}
	path = strings.TrimPrefix(path, "/")
	parts := strings.SplitN(path, "/", 2)
	if len(parts) == 2 {
		return parts[1], nil
	}
	return path, nil
}

func (s *Server) replaceMetadata(r *http.Request, key string, meta storage.ObjectMeta) (*storage.ObjectMeta, error) {
	return s.store.ReplaceMetadata(r.Context(), key, meta)
}

func checkCopyConditions(r *http.Request, m *storage.ObjectMeta) (string, bool) {
	h := r.Header
	if v := h.Get("X-Amz-Copy-Source-If-Match"); v != "" {
		if !etagMatches(v, m.ETag) {
			return "PreconditionFailed", false
		}
	}
	if v := h.Get("X-Amz-Copy-Source-If-None-Match"); v != "" {
		if etagMatches(v, m.ETag) {
			return "PreconditionFailed", false
		}
	}
	if v := h.Get("X-Amz-Copy-Source-If-Modified-Since"); v != "" {
		if t, err := http.ParseTime(v); err == nil && !m.LastModified.After(t) {
			return "PreconditionFailed", false
		}
	}
	if v := h.Get("X-Amz-Copy-Source-If-Unmodified-Since"); v != "" {
		if t, err := http.ParseTime(v); err == nil && m.LastModified.After(t) {
			return "PreconditionFailed", false
		}
	}
	return "", true
}
