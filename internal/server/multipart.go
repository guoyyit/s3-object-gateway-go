package server

import (
	"encoding/xml"
	"io"
	"net/http"
	"strconv"

	"github.com/guoyyit/s3-object-gateway-go/internal/protocol"
)

type initiateMultipartUploadResult struct {
	XMLName  xml.Name `xml:"InitiateMultipartUploadResult"`
	Xmlns    string   `xml:"xmlns,attr"`
	Bucket   string   `xml:"Bucket"`
	Key      string   `xml:"Key"`
	UploadID string   `xml:"UploadId"`
}

type completeMultipartUploadInput struct {
	XMLName xml.Name             `xml:"CompleteMultipartUpload"`
	Parts   []completedPartInput `xml:"Part"`
}

type completedPartInput struct {
	PartNumber int    `xml:"PartNumber"`
	ETag       string `xml:"ETag"`
}

type completeMultipartUploadResult struct {
	XMLName  xml.Name `xml:"CompleteMultipartUploadResult"`
	Xmlns    string   `xml:"xmlns,attr"`
	Location string   `xml:"Location"`
	Bucket   string   `xml:"Bucket"`
	Key      string   `xml:"Key"`
	ETag     string   `xml:"ETag"`
}

func (s *Server) handleMultipartLifecycle(w http.ResponseWriter, r *http.Request, bucket, key, requestID string) {
	switch r.Method {
	case http.MethodPost:
		s.completeMultipart(w, r, bucket, key, requestID)
	case http.MethodPut:
		s.createMultipart(w, r, bucket, key, requestID)
	case http.MethodDelete:
		s.abortMultipart(w, r, bucket, key, requestID)
	default:
		protocol.WriteError(w, r, protocol.ErrMethodNotAllowed, requestID)
	}
}

func (s *Server) createMultipart(w http.ResponseWriter, r *http.Request, bucket, key, requestID string) {
	up, err := s.store.CreateMultipartUpload(r.Context(), bucket, key, putOptions(r))
	if err != nil {
		protocol.WriteError(w, r, err, requestID)
		return
	}
	writeXML(w, http.StatusOK, initiateMultipartUploadResult{
		Xmlns:    "http://s3.amazonaws.com/doc/2006-03-01/",
		Bucket:   bucket,
		Key:      key,
		UploadID: up.UploadID,
	})
}

func (s *Server) abortMultipart(w http.ResponseWriter, r *http.Request, bucket, key, requestID string) {
	uploadID := r.URL.Query().Get("uploadId")
	if err := s.store.AbortMultipartUpload(r.Context(), bucket, key, uploadID); err != nil {
		protocol.WriteError(w, r, err, requestID)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) completeMultipart(w http.ResponseWriter, r *http.Request, bucket, key, requestID string) {
	uploadID := r.URL.Query().Get("uploadId")
	data, err := io.ReadAll(r.Body)
	if err != nil {
		protocol.WriteError(w, r, err, requestID)
		return
	}
	var input completeMultipartUploadInput
	if err := xml.Unmarshal(data, &input); err != nil {
		protocol.WriteError(w, r, protocol.WithMessage(protocol.ErrInvalidArgument, err.Error()), requestID)
		return
	}
	parts := make([]int, 0, len(input.Parts))
	clientETags := map[int]string{}
	for _, p := range input.Parts {
		parts = append(parts, p.PartNumber)
		clientETags[p.PartNumber] = unquoteETag(p.ETag)
	}

	info, err := s.store.CompleteMultipartUpload(r.Context(), bucket, key, uploadID, parts, clientETags)
	if err != nil {
		protocol.WriteError(w, r, err, requestID)
		return
	}
	writeXML(w, http.StatusOK, completeMultipartUploadResult{
		Xmlns:  "http://s3.amazonaws.com/doc/2006-03-01/",
		Bucket: bucket,
		Key:    key,
		ETag:   quoteETag(info.ETag),
	})
}

func (s *Server) handleUploadPartOrList(w http.ResponseWriter, r *http.Request, bucket, key, requestID string) {
	uploadID := r.URL.Query().Get("uploadId")
	switch r.Method {
	case http.MethodPut:
		partNumber, err := strconv.Atoi(r.URL.Query().Get("partNumber"))
		if err != nil || partNumber < 1 {
			protocol.WriteError(w, r, protocol.ErrInvalidArgument, requestID)
			return
		}
		part, perr := s.store.UploadPart(r.Context(), bucket, key, uploadID, partNumber, r.Body)
		if perr != nil {
			protocol.WriteError(w, r, perr, requestID)
			return
		}
		w.Header().Set("ETag", quoteETag(part.ETag))
		w.WriteHeader(http.StatusOK)
	case http.MethodGet:
		s.listParts(w, r, bucket, key, uploadID, requestID)
	default:
		protocol.WriteError(w, r, protocol.ErrMethodNotAllowed, requestID)
	}
}

type listPartsResult struct {
	XMLName  xml.Name        `xml:"ListPartsResult"`
	Xmlns    string          `xml:"xmlns,attr"`
	Bucket   string          `xml:"Bucket"`
	Key      string          `xml:"Key"`
	UploadID string          `xml:"UploadId"`
	Part     []listedPartXML `xml:"Part"`
}

type listedPartXML struct {
	PartNumber   int    `xml:"PartNumber"`
	LastModified string `xml:"LastModified"`
	ETag         string `xml:"ETag"`
	Size         int64  `xml:"Size"`
}

func (s *Server) listParts(w http.ResponseWriter, r *http.Request, bucket, key, uploadID, requestID string) {
	parts, err := s.store.ListParts(r.Context(), bucket, key, uploadID)
	if err != nil {
		protocol.WriteError(w, r, err, requestID)
		return
	}
	out := listPartsResult{
		Xmlns:    "http://s3.amazonaws.com/doc/2006-03-01/",
		Bucket:   bucket,
		Key:      key,
		UploadID: uploadID,
	}
	for _, p := range parts {
		out.Part = append(out.Part, listedPartXML{
			PartNumber: p.PartNumber,
			ETag:       quoteETag(p.ETag),
			Size:       p.Size,
		})
	}
	writeXML(w, http.StatusOK, out)
}

func unquoteETag(v string) string {
	if len(v) >= 2 && v[0] == '"' && v[len(v)-1] == '"' {
		return v[1 : len(v)-1]
	}
	return v
}
