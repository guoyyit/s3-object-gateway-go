package api

import (
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"

	s3err "github.com/guoyyit/s3-object-gateway-go/internal/s3error"
	"github.com/guoyyit/s3-object-gateway-go/internal/storage"
)

// handleObject dispatches operations addressed at a single object key.
func (s *Server) handleObject(w http.ResponseWriter, r *http.Request, key string) {
	switch r.Method {
	case http.MethodPut:
		if r.Header.Get("X-Amz-Copy-Source") != "" {
			s.handleCopy(w, r, key)
			return
		}
		s.handlePutObject(w, r, key)
	case http.MethodPost:
		if r.URL.Query().Get("uploads") != "" {
			s.handleInitiateMultipart(w, r, key)
			return
		}
		s.respondError(w, r, s3err.New("MethodNotAllowed"))
	case http.MethodGet:
		s.handleGetObject(w, r, key)
	case http.MethodHead:
		s.handleHeadObject(w, r, key)
	case http.MethodDelete:
		s.handleDeleteObject(w, r, key)
	default:
		s.respondError(w, r, s3err.New("MethodNotAllowed"))
	}
}

func (s *Server) handleObjectAction(w http.ResponseWriter, r *http.Request, key, action string) {
	switch action {
	case "uploadpart":
		s.handleUploadPartOrComplete(w, r, key)
	case "initiate-upload":
		if r.Method == http.MethodPost {
			s.handleInitiateMultipart(w, r, key)
			return
		}
		s.respondError(w, r, s3err.New("MethodNotAllowed"))
	case "unsupported":
		s.respondError(w, r, s3err.New("NotImplemented"))
	default:
		s.respondError(w, r, s3err.New("NotImplemented"))
	}
}

func (s *Server) handlePutObject(w http.ResponseWriter, r *http.Request, key string) {
	if rejectUnsupportedPutHeaders(w, r) {
		return
	}
	meta := objectMetaFromRequest(r, key)
	out, err := s.store.PutObject(r.Context(), key, r.Body, meta)
	if err != nil {
		s.respondError(w, r, mapStoreError(err))
		return
	}
	h := w.Header()
	h.Set("ETag", out.ETag)
	h.Set("x-amz-version-id", outVersionID())
	w.WriteHeader(http.StatusOK)
}

func (s *Server) handleGetObject(w http.ResponseWriter, r *http.Request, key string) {
	meta, err := s.store.HeadObject(r.Context(), key)
	if err != nil {
		s.respondError(w, r, mapStoreError(err))
		return
	}
	if code, ok := checkReadConditions(r, meta); !ok {
		if code == "NotModified" {
			writeCommonHeaders(w, meta)
			w.WriteHeader(http.StatusNotModified)
			return
		}
		s.respondError(w, r, s3err.New(code))
		return
	}

	var rng *storage.Range
	rangeHeader := r.Header.Get("Range")
	if rangeHeader != "" {
		rng, err = storage.ParseRange(rangeHeader, meta.Size)
		if err != nil {
			s.respondError(w, r, s3err.New("InvalidRange"))
			return
		}
	}

	reader, err := s.store.GetObject(r.Context(), key, rng)
	if err != nil {
		s.respondError(w, r, mapStoreError(err))
		return
	}
	defer reader.Body.Close()

	writeCommonHeaders(w, &reader.Meta)
	if rng != nil {
		w.Header().Set("Content-Length", strconv.FormatInt(reader.Meta.Size, 10))
		w.Header().Set("Content-Range",
			fmt.Sprintf("bytes %d-%d/%d", rng.Start, rng.End, meta.Size))
		w.WriteHeader(http.StatusPartialContent)
	} else {
		w.Header().Set("Content-Length", strconv.FormatInt(reader.Meta.Size, 10))
		w.WriteHeader(http.StatusOK)
	}
	if r.Method == http.MethodHead {
		return
	}
	if _, err := io.Copy(w, reader.Body); err != nil {
		return
	}
}

func (s *Server) handleHeadObject(w http.ResponseWriter, r *http.Request, key string) {
	meta, err := s.store.HeadObject(r.Context(), key)
	if err != nil {
		s.respondError(w, r, mapStoreError(err))
		return
	}
	writeCommonHeaders(w, meta)
	w.Header().Set("Content-Length", strconv.FormatInt(meta.Size, 10))
	w.Header().Set("x-amz-version-id", outVersionID())
	w.WriteHeader(http.StatusOK)
}

func (s *Server) handleDeleteObject(w http.ResponseWriter, r *http.Request, key string) {
	if err := s.store.DeleteObject(r.Context(), key); err != nil {
		s.respondError(w, r, mapStoreError(err))
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// checkReadConditions evaluates If-Match / If-None-Match /
// If-Modified-Since / If-Unmodified-Since. Returns ("", true) when the
// request may proceed, otherwise an S3 error code.
func checkReadConditions(r *http.Request, m *storage.ObjectMeta) (string, bool) {
	h := r.Header
	if v := h.Get("If-Match"); v != "" {
		if !etagMatches(v, m.ETag) {
			return "PreconditionFailed", false
		}
	}
	if v := h.Get("If-Unmodified-Since"); v != "" {
		if t, err := http.ParseTime(v); err == nil && m.LastModified.After(t) {
			return "PreconditionFailed", false
		}
	}
	if v := h.Get("If-None-Match"); v != "" {
		if etagMatches(v, m.ETag) {
			return "NotModified", false
		}
	}
	if v := h.Get("If-Modified-Since"); v != "" {
		if t, err := http.ParseTime(v); err == nil && !m.LastModified.After(t) {
			return "NotModified", false
		}
	}
	return "", true
}

func etagMatches(pattern, etag string) bool {
	pattern = strings.TrimSpace(pattern)
	if pattern == "*" {
		return true
	}
	for _, p := range strings.Split(pattern, ",") {
		if strings.TrimSpace(p) == etag {
			return true
		}
	}
	return false
}

// rejectUnsupportedPutHeaders rejects requests that request unimplemented
// features with a proper error instead of silently dropping them.
func rejectUnsupportedPutHeaders(w http.ResponseWriter, r *http.Request) bool {
	for _, h := range []string{
		"X-Amz-Server-Side-Encryption",
		"X-Amz-Server-Side-Encryption-Customer-Algorithm",
		"X-Amz-Object-Lock-Mode",
		"X-Amz-Object-Lock-Retain-Until-Date",
		"X-Amz-Object-Lock-Legal-Hold",
	} {
		if r.Header.Get(h) != "" {
			s := s3err.New("NotImplemented")
			s.Message = "the requested feature is not implemented"
			s3err.WriteXML(w, r, s)
			return true
		}
	}
	return false
}

func outVersionID() string { return "1" }
