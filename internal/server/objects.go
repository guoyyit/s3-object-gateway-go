package server

import (
	"io"
	"net/http"
	"strconv"
	"strings"

	"github.com/guoyyit/s3-object-gateway-go/internal/protocol"
	"github.com/guoyyit/s3-object-gateway-go/internal/storage"
)

const timeFormat = "2006-01-02T15:04:05.000Z"

func putOptions(r *http.Request) storage.PutOptions {
	return storage.PutOptions{
		ContentType:   r.Header.Get("Content-Type"),
		ContentMD5B64: strings.TrimSpace(r.Header.Get("Content-MD5")),
		UserMetadata:  extractUserMetadata(r),
	}
}

func extractUserMetadata(r *http.Request) map[string]string {
	out := map[string]string{}
	for name, vals := range r.Header {
		if strings.HasPrefix(strings.ToLower(name), "x-amz-meta-") && len(vals) > 0 {
			out[name[len("x-amz-meta-"):]] = vals[0]
		}
	}
	return out
}

func (s *Server) handlePutObject(w http.ResponseWriter, r *http.Request, bucket, key, requestID string) {
	opts := putOptions(r)
	info, err := s.store.PutObject(r.Context(), bucket, key, r.Body, opts)
	if err != nil {
		protocol.WriteError(w, r, err, requestID)
		return
	}
	w.Header().Set("ETag", quoteETag(info.ETag))
	w.WriteHeader(http.StatusOK)
}

func (s *Server) handleDeleteObject(w http.ResponseWriter, r *http.Request, bucket, key, requestID string) {
	if err := s.store.DeleteObject(r.Context(), bucket, key); err != nil {
		protocol.WriteError(w, r, err, requestID)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleHeadObject(w http.ResponseWriter, r *http.Request, bucket, key, requestID string) {
	info, err := s.store.HeadObject(r.Context(), bucket, key)
	if err != nil {
		if s3err, ok := err.(*protocol.S3Error); ok && s3err.StatusCode == http.StatusNotFound {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		protocol.WriteError(w, r, err, requestID)
		return
	}
	writeObjectHeaders(w, info)
	if notModified, err := evaluateObjectConditions(r, info); err != nil {
		protocol.WriteError(w, r, err, requestID)
		return
	} else if notModified {
		w.WriteHeader(http.StatusNotModified)
		return
	}
	w.WriteHeader(http.StatusOK)
}

func (s *Server) handleGetObject(w http.ResponseWriter, r *http.Request, bucket, key, requestID string) {
	reader, err := s.store.GetObject(r.Context(), bucket, key)
	if err != nil {
		protocol.WriteError(w, r, err, requestID)
		return
	}
	defer reader.Close()

	if notModified, cerr := evaluateObjectConditions(r, &reader.Info); cerr != nil {
		protocol.WriteError(w, r, cerr, requestID)
		return
	} else if notModified {
		writeObjectHeaders(w, &reader.Info)
		w.WriteHeader(http.StatusNotModified)
		return
	}

	br, hasRange, perr := parseRange(r.Header.Get("Range"), reader.Info.Size)
	if perr != nil {
		protocol.WriteError(w, r, perr, requestID)
		return
	}

	writeObjectHeaders(w, &reader.Info)
	if !hasRange {
		w.WriteHeader(http.StatusOK)
		_, _ = io.Copy(w, reader)
		return
	}

	if _, err := reader.Seek(br.start, io.SeekStart); err != nil {
		protocol.WriteError(w, r, err, requestID)
		return
	}
	length := br.end - br.start + 1
	w.Header().Set("Content-Range", "bytes "+strconv.FormatInt(br.start, 10)+"-"+
		strconv.FormatInt(br.end, 10)+"/"+strconv.FormatInt(reader.Info.Size, 10))
	w.Header().Set("Content-Length", strconv.FormatInt(length, 10))
	w.WriteHeader(http.StatusPartialContent)
	_, _ = io.CopyN(w, reader, length)
}

func writeObjectHeaders(w http.ResponseWriter, info *storage.ObjectInfo) {
	w.Header().Set("ETag", quoteETag(info.ETag))
	w.Header().Set("Last-Modified", info.LastModified.UTC().Format(http.TimeFormat))
	w.Header().Set("Accept-Ranges", "bytes")
	w.Header().Set("Content-Length", strconv.FormatInt(info.Size, 10))
	if info.ContentType != "" {
		w.Header().Set("Content-Type", info.ContentType)
	} else {
		w.Header().Set("Content-Type", "binary/octet-stream")
	}
	for k, v := range info.UserMetadata {
		w.Header().Set("X-Amz-Meta-"+k, v)
	}
}

func quoteETag(etag string) string { return "\"" + etag + "\"" }

// evaluateObjectConditions handles If-Match / If-None-Match /
// If-Modified-Since / If-Unmodified-Since.
func evaluateObjectConditions(r *http.Request, info *storage.ObjectInfo) (bool, error) {
	if v := r.Header.Get("If-Match"); v != "" && !etagMatches(info.ETag, v) {
		return false, protocol.ErrPreconditionFailed
	}
	if v := r.Header.Get("If-None-Match"); v != "" {
		if v == "*" || etagMatches(info.ETag, v) {
			if r.Method == http.MethodHead || r.Method == http.MethodGet {
				return true, nil
			}
			return false, protocol.ErrPreconditionFailed
		}
	}
	if v := r.Header.Get("If-Unmodified-Since"); v != "" {
		t, err := http.ParseTime(v)
		if err == nil && info.LastModified.After(t) {
			return false, protocol.ErrPreconditionFailed
		}
	}
	if v := r.Header.Get("If-Modified-Since"); v != "" {
		t, err := http.ParseTime(v)
		if err == nil && !info.LastModified.After(t) {
			return true, nil
		}
	}
	return false, nil
}

func etagMatches(etag, header string) bool {
	parts := strings.Split(header, ",")
	for _, p := range parts {
		p = strings.TrimSpace(p)
		p = strings.TrimPrefix(p, "W/")
		p = strings.Trim(p, "\"")
		if p == etag {
			return true
		}
	}
	return false
}
