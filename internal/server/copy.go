package server

import (
	"encoding/xml"
	"net/http"
	"net/url"
	"strings"

	"github.com/guoyyit/s3-object-gateway-go/internal/protocol"
	"github.com/guoyyit/s3-object-gateway-go/internal/storage"
)

type copyObjectResult struct {
	XMLName      xml.Name `xml:"CopyObjectResult"`
	LastModified string   `xml:"LastModified"`
	ETag         string   `xml:"ETag"`
}

func (s *Server) handleCopyObject(w http.ResponseWriter, r *http.Request, bucket, key, requestID string) {
	srcBucket, srcKey, err := parseCopySource(r.Header.Get("X-Amz-Copy-Source"))
	if err != nil {
		protocol.WriteError(w, r, protocol.WithMessage(protocol.ErrInvalidArgument, err.Error()), requestID)
		return
	}
	opts := storage.CopyOptions{
		CopySourceIfMatch:     r.Header.Get("X-Amz-Copy-Source-If-Match"),
		CopySourceIfNoneMatch: r.Header.Get("X-Amz-Copy-Source-If-None-Match"),
		ContentType:           r.Header.Get("Content-Type"),
		MetadataDirective:     strings.ToUpper(r.Header.Get("X-Amz-Metadata-Directive")),
		UserMetadata:          extractUserMetadata(r),
	}
	if v := r.Header.Get("X-Amz-Copy-Source-If-Modified-Since"); v != "" {
		if t, perr := http.ParseTime(v); perr == nil {
			opts.CopySourceIfModifiedSince = t
		}
	}
	if v := r.Header.Get("X-Amz-Copy-Source-If-Unmodified-Since"); v != "" {
		if t, perr := http.ParseTime(v); perr == nil {
			opts.CopySourceIfUnmodifiedSince = t
		}
	}

	info, cerr := s.store.CopyObject(r.Context(), bucket, key, srcBucket, srcKey, opts)
	if cerr != nil {
		protocol.WriteError(w, r, cerr, requestID)
		return
	}
	writeXML(w, http.StatusOK, copyObjectResult{
		LastModified: info.LastModified.UTC().Format(timeFormat),
		ETag:         quoteETag(info.ETag),
	})
}

// parseCopySource parses "/bucket/key" (URL encoded) from x-amz-copy-source.
func parseCopySource(raw string) (bucket, key string, err error) {
	raw = strings.TrimPrefix(raw, "/")
	if i := strings.Index(raw, "?"); i >= 0 {
		raw = raw[:i]
	}
	decoded, err := url.PathUnescape(raw)
	if err != nil {
		return "", "", err
	}
	parts := strings.SplitN(decoded, "/", 2)
	if len(parts) < 2 || parts[0] == "" || parts[1] == "" {
		return "", "", protocol.WithMessage(protocol.ErrInvalidArgument, "copy source must be /bucket/key")
	}
	return parts[0], parts[1], nil
}
