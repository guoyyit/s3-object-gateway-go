package api

import (
	"encoding/xml"
	"io"
	"net/http"
	"strconv"
	"strings"

	s3err "github.com/guoyyit/s3-object-gateway-go/internal/s3error"
	"github.com/guoyyit/s3-object-gateway-go/internal/storage"
)

// POST /key?uploads
func (s *Server) handleInitiateMultipart(w http.ResponseWriter, r *http.Request, key string) {
	meta := objectMetaFromRequest(r, key)
	id, err := s.store.InitiateMultipart(r.Context(), key, meta)
	if err != nil {
		s.respondError(w, r, mapStoreError(err))
		return
	}
	writeXML(w, http.StatusOK, initiateMultipartResult{
		Xmlns:    s3NS,
		Bucket:   bucketName,
		Key:      key,
		UploadID: id,
	})
}

// PUT/DELETE/GET /key?uploadId=...
func (s *Server) handleUploadPartOrComplete(w http.ResponseWriter, r *http.Request, key string) {
	uploadID := r.URL.Query().Get("uploadId")
	switch r.Method {
	case http.MethodPut:
		partNumberStr := r.URL.Query().Get("partNumber")
		if partNumberStr == "" {
			s.handleCompleteMultipart(w, r, key, uploadID)
			return
		}
		partNumber, err := strconv.Atoi(partNumberStr)
		if err != nil {
			s.respondError(w, r, s3err.New("InvalidArgument"))
			return
		}
		part, err := s.store.UploadPart(r.Context(), uploadID, partNumber, r.Body)
		if err != nil {
			s.respondError(w, r, mapStoreError(err))
			return
		}
		w.Header().Set("ETag", part.ETag)
		w.WriteHeader(http.StatusOK)
	case http.MethodPost:
		s.handleCompleteMultipart(w, r, key, uploadID)
	case http.MethodDelete:
		if err := s.store.AbortMultipart(r.Context(), uploadID); err != nil {
			s.respondError(w, r, s3err.New("NoSuchUpload"))
			return
		}
		w.WriteHeader(http.StatusNoContent)
	case http.MethodGet, http.MethodHead:
		s.handleListParts(w, r, key, uploadID)
	default:
		s.respondError(w, r, s3err.New("MethodNotAllowed"))
	}
}

func (s *Server) handleCompleteMultipart(w http.ResponseWriter, r *http.Request, key, uploadID string) {
	body, err := io.ReadAll(r.Body)
	if err != nil {
		s.respondError(w, r, s3err.New("InternalError"))
		return
	}
	var req completeMultipartUpload
	if err := xml.Unmarshal(body, &req); err != nil {
		s.respondError(w, r, s3err.New("MalformedXML"))
		return
	}
	parts := make([]storage.PartInfo, 0, len(req.Parts))
	for _, p := range req.Parts {
		parts = append(parts, storage.PartInfo{PartNumber: p.PartNumber, ETag: strings.TrimSpace(p.ETag)})
	}
	out, err := s.store.CompleteMultipart(r.Context(), uploadID, parts)
	if err != nil {
		s.respondError(w, r, mapStoreError(err))
		return
	}
	w.Header().Set("ETag", out.ETag)
	w.Header().Set("x-amz-version-id", outVersionID())
	writeXML(w, http.StatusOK, completeMultipartResult{
		Xmlns:    s3NS,
		Location: "/" + bucketName + "/" + key,
		Bucket:   bucketName,
		Key:      key,
		ETag:     out.ETag,
	})
}

func (s *Server) handleListParts(w http.ResponseWriter, r *http.Request, key, uploadID string) {
	parts, err := s.store.ListParts(r.Context(), uploadID)
	if err != nil {
		s.respondError(w, r, s3err.New("NoSuchUpload"))
		return
	}
	result := listPartsResult{
		Xmlns:        s3NS,
		Bucket:       bucketName,
		Key:          key,
		UploadID:     uploadID,
		Initiator:    ownerXML{ID: ownerID, DisplayName: ownerID},
		Owner:        ownerXML{ID: ownerID, DisplayName: ownerID},
		StorageClass: "STANDARD",
		MaxParts:     1000,
	}
	for _, p := range parts {
		result.Part = append(result.Part, listPartXML{
			PartNumber:   p.PartNumber,
			ETag:         p.ETag,
			Size:         p.Size,
			LastModified: "2024-01-01T00:00:00.000Z",
		})
	}
	writeXML(w, http.StatusOK, result)
}

// GET /?uploads
func (s *Server) handleListMultipartUploads(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	maxUploads := 1000
	if v := q.Get("max-uploads"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			maxUploads = n
		}
	}
	ups, next, truncated, err := s.store.ListMultipartUploads(
		r.Context(), q.Get("prefix"), q.Get("delimiter"), q.Get("key-marker"), maxUploads)
	if err != nil {
		s.respondError(w, r, mapStoreError(err))
		return
	}
	result := listMultipartUploadsResult{
		Xmlns:         s3NS,
		Bucket:        bucketName,
		MaxUploads:    maxUploads,
		IsTruncated:   truncated,
		NextKeyMarker: next,
	}
	for _, u := range ups {
		result.Upload = append(result.Upload, uploadXML{
			Key:          u.Key,
			UploadID:     u.UploadID,
			Initiator:    ownerXML{ID: ownerID, DisplayName: ownerID},
			Owner:        ownerXML{ID: ownerID, DisplayName: ownerID},
			StorageClass: "STANDARD",
			Initiated:    u.Initiated.UTC().Format("2006-01-02T15:04:05.000Z"),
		})
	}
	writeXML(w, http.StatusOK, result)
}
