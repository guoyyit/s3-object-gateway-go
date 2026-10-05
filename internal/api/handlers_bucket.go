package api

import (
	"encoding/xml"
	"io"
	"net/http"
	"strconv"

	s3err "github.com/guoyyit/s3-object-gateway-go/internal/s3error"
)

const bucketName = "local"
const ownerID = "local-user"

// GET / -> ListBuckets. A single fixed bucket is exposed.
func (s *Server) handleListBuckets(w http.ResponseWriter, r *http.Request) {
	res := listAllMyBucketsResult{
		Xmlns: s3NS,
		Owner: ownerXML{ID: ownerID, DisplayName: ownerID},
		Buckets: bucketsXML{
			Bucket: []bucketXML{{Name: bucketName, CreationDate: "2024-01-01T00:00:00.000Z"}},
		},
	}
	writeXML(w, http.StatusOK, res)
}

// HEAD / -> the single bucket always exists.
func (s *Server) handleBucketHead(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("x-amz-bucket-region", "us-east-1")
	w.WriteHeader(http.StatusOK)
}

// GET /?prefix=... -> ListObjectsV1 (and ListObjectsV2 via list-type=2).
func (s *Server) handleListObjects(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	prefix := q.Get("prefix")
	delimiter := q.Get("delimiter")

	var marker string
	var maxKeys int
	var startAfter string
	v2 := q.Get("list-type") == "2"
	if v2 {
		marker = q.Get("continuation-token")
		startAfter = q.Get("start-after")
		if marker == "" && startAfter != "" {
			marker = startAfter
		}
	} else {
		marker = q.Get("marker")
	}
	maxKeys = 1000
	if v := q.Get("max-keys"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			maxKeys = n
		}
	}

	page, err := s.store.ListObjects(r.Context(), prefix, delimiter, marker, maxKeys)
	if err != nil {
		s.respondError(w, r, mapStoreError(err))
		return
	}

	result := listBucketResult{
		Xmlns:          s3NS,
		Name:           bucketName,
		Prefix:         prefix,
		Marker:         marker,
		MaxKeys:        maxKeys,
		Delimiter:      delimiter,
		IsTruncated:    page.IsTruncated,
		NextMarker:     page.NextMarker,
		KeyCount:       len(page.Contents) + len(page.CommonPrefixes),
		CommonPrefixes: nil,
	}
	for _, cp := range page.CommonPrefixes {
		result.CommonPrefixes = append(result.CommonPrefixes, commonPrefix{Prefix: cp})
	}
	for _, c := range page.Contents {
		result.Contents = append(result.Contents, contentXML{
			Key:          c.Key,
			LastModified: c.LastModified.UTC().Format("2006-01-02T15:04:05.000Z"),
			ETag:         c.ETag,
			Size:         c.Size,
			StorageClass: "STANDARD",
		})
	}
	writeXML(w, http.StatusOK, result)
}

// POST /?delete -> DeleteObjects batch deletion.
func (s *Server) handleBatchDelete(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(r.Body)
	if err != nil {
		s.respondError(w, r, s3err.New("InternalError"))
		return
	}
	var req deleteRequest
	if err := xml.Unmarshal(body, &req); err != nil {
		s.respondError(w, r, s3err.New("MalformedXML"))
		return
	}
	res := deleteResult{Xmlns: s3NS}
	for _, o := range req.Objects {
		if err := s.store.DeleteObject(r.Context(), o.Key); err != nil {
			s.respondError(w, r, mapStoreError(err))
			return
		}
		if !req.Quiet {
			res.Deleted = append(res.Deleted, deletedKey{Key: o.Key})
		}
	}
	writeXML(w, http.StatusOK, res)
}

// handleBucketAction dispatches query sub-resources on the bucket root.
func (s *Server) handleBucketAction(w http.ResponseWriter, r *http.Request, action string) {
	switch action {
	case "uploads":
		s.handleListMultipartUploads(w, r)
	case "unsupported":
		s.respondError(w, r, s3err.New("NotImplemented"))
	default:
		s.respondError(w, r, s3err.New("NotImplemented"))
	}
}
