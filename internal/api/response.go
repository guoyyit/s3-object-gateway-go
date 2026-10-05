package api

import (
	"errors"
	"net/http"

	s3err "github.com/guoyyit/s3-object-gateway-go/internal/s3error"
	"github.com/guoyyit/s3-object-gateway-go/internal/storage"
)

func (s *Server) respondError(w http.ResponseWriter, r *http.Request, e *s3err.Error) {
	e.RequestID = w.Header().Get("X-Amz-Request-Id")
	s3err.WriteXML(w, r, e)
}

// mapStoreError converts storage sentinel errors to S3 protocol errors.
func mapStoreError(err error) *s3err.Error {
	switch {
	case isStoreErr(err, storage.ErrNotFound):
		return s3err.New("NoSuchKey")
	case isStoreErr(err, storage.ErrInvalidRange):
		return s3err.New("InvalidRange")
	case isStoreErr(err, storage.ErrInvalidPart):
		return s3err.New("InvalidPart")
	case isStoreErr(err, storage.ErrInvalidPartOrder):
		return s3err.New("InvalidPartOrder")
	case isStoreErr(err, storage.ErrInvalidPartNumber):
		return s3err.New("InvalidArgument")
	case isStoreErr(err, storage.ErrPartTooSmall):
		return s3err.New("EntityTooSmall")
	default:
		return s3err.New("InternalError")
	}
}

func isStoreErr(err, target error) bool {
	return errors.Is(err, target)
}

const amzMetaPrefix = "X-Amz-Meta-"

// extractMetadata collects user metadata headers (X-Amz-Meta-*) preserving
// the lowercased suffix keys as S3 does.
func extractMetadata(h http.Header) map[string]string {
	var out map[string]string
	for k, vs := range h {
		if len(k) > len(amzMetaPrefix) && equalFold(k[:len(amzMetaPrefix)], amzMetaPrefix) {
			if out == nil {
				out = map[string]string{}
			}
			out[k[len(amzMetaPrefix):]] = vs[0]
		}
	}
	return out
}

func equalFold(a, b string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := 0; i < len(a); i++ {
		ca, cb := a[i], b[i]
		if ca >= 'A' && ca <= 'Z' {
			ca += 'a' - 'A'
		}
		if cb >= 'A' && cb <= 'Z' {
			cb += 'a' - 'A'
		}
		if ca != cb {
			return false
		}
	}
	return true
}

// writeCommonHeaders sets the standard system/metadata response headers.
func writeCommonHeaders(w http.ResponseWriter, m *storage.ObjectMeta) {
	h := w.Header()
	if m.ContentType != "" {
		h.Set("Content-Type", m.ContentType)
	}
	if m.ContentEncoding != "" {
		h.Set("Content-Encoding", m.ContentEncoding)
	}
	if m.ContentLanguage != "" {
		h.Set("Content-Language", m.ContentLanguage)
	}
	if m.ContentDisposition != "" {
		h.Set("Content-Disposition", m.ContentDisposition)
	}
	if m.CacheControl != "" {
		h.Set("Cache-Control", m.CacheControl)
	}
	if !m.Expires.IsZero() {
		h.Set("Expires", m.Expires.UTC().Format(http.TimeFormat))
	}
	h.Set("ETag", m.ETag)
	h.Set("Last-Modified", m.LastModified.UTC().Format(http.TimeFormat))
	h.Set("x-amz-request-id", h.Get("X-Amz-Request-Id"))
	for k, v := range m.Metadata {
		h.Set(amzMetaPrefix+k, v)
	}
}

// objectMetaFromRequest builds storage metadata from request headers.
func objectMetaFromRequest(r *http.Request, key string) storage.ObjectMeta {
	h := r.Header
	m := storage.ObjectMeta{
		Key:                key,
		ContentType:        firstNonEmpty(h.Get("Content-Type"), "application/octet-stream"),
		ContentEncoding:    h.Get("Content-Encoding"),
		ContentLanguage:    h.Get("Content-Language"),
		ContentDisposition: h.Get("Content-Disposition"),
		CacheControl:       h.Get("Cache-Control"),
		StorageClass:       firstNonEmpty(h.Get("X-Amz-Storage-Class"), "STANDARD"),
		Metadata:           extractMetadata(h),
	}
	if v := h.Get("Expires"); v != "" {
		if t, err := http.ParseTime(v); err == nil {
			m.Expires = t
		}
	}
	return m
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}
