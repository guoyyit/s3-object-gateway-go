package server

import (
	"encoding/xml"
	"net/http"
	"strconv"

	"github.com/guoyyit/s3-object-gateway-go/internal/protocol"
)

type listAllMyBucketsResult struct {
	XMLName xml.Name `xml:"ListAllMyBucketsResult"`
	Xmlns   string   `xml:"xmlns,attr"`
	Owner   owner
	Buckets buckets `xml:"Buckets"`
}

type owner struct {
	ID          string `xml:"ID"`
	DisplayName string `xml:"DisplayName"`
}

type buckets struct {
	Bucket []bucketXML `xml:"Bucket"`
}

type bucketXML struct {
	Name         string `xml:"Name"`
	CreationDate string `xml:"CreationDate"`
}

type bucketLocationResult struct {
	XMLName xml.Name `xml:"LocationConstraint"`
	Xmlns   string   `xml:"xmlns,attr"`
	Region  string   `xml:",chardata"`
}

func (s *Server) handleBucketLocation(w http.ResponseWriter, r *http.Request, bucket, requestID string) {
	exists, err := s.store.BucketExists(r.Context(), bucket)
	if err != nil {
		protocol.WriteError(w, r, err, requestID)
		return
	}
	if !exists {
		protocol.WriteError(w, r, protocol.ErrNoSuchBucket, requestID)
		return
	}
	writeXML(w, http.StatusOK, bucketLocationResult{
		Xmlns:  "http://s3.amazonaws.com/doc/2006-03-01/",
		Region: "us-east-1",
	})
}

func (s *Server) handleListBuckets(w http.ResponseWriter, r *http.Request, requestID string) {
	if r.Method != http.MethodGet {
		protocol.WriteError(w, r, protocol.ErrMethodNotAllowed, requestID)
		return
	}
	names, err := s.store.ListBuckets(r.Context())
	if err != nil {
		protocol.WriteError(w, r, err, requestID)
		return
	}
	result := listAllMyBucketsResult{
		Xmlns: "http://s3.amazonaws.com/doc/2006-03-01/",
		Owner: owner{ID: "local", DisplayName: "local"},
	}
	for _, name := range names {
		result.Buckets.Bucket = append(result.Buckets.Bucket, bucketXML{
			Name:         name,
			CreationDate: "2024-01-01T00:00:00.000Z",
		})
	}
	writeXML(w, http.StatusOK, result)
}

func (s *Server) handleBucket(w http.ResponseWriter, r *http.Request, bucket, requestID string) {
	switch r.Method {
	case http.MethodPut:
		err := s.store.CreateBucket(r.Context(), bucket)
		if err != nil {
			protocol.WriteError(w, r, err, requestID)
			return
		}
		w.Header().Set("Location", "/"+bucket)
		w.WriteHeader(http.StatusOK)
	case http.MethodDelete:
		if err := s.store.DeleteBucket(r.Context(), bucket); err != nil {
			protocol.WriteError(w, r, err, requestID)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	case http.MethodHead:
		exists, err := s.store.BucketExists(r.Context(), bucket)
		if err != nil {
			protocol.WriteError(w, r, err, requestID)
			return
		}
		if !exists {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.WriteHeader(http.StatusOK)
	default:
		protocol.WriteError(w, r, protocol.ErrMethodNotAllowed, requestID)
	}
}

func writeXML(w http.ResponseWriter, status int, v interface{}) {
	out, err := xml.MarshalIndent(v, "", "  ")
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	out = append([]byte(xml.Header), out...)
	w.Header().Set("Content-Type", "application/xml")
	w.Header().Set("Content-Length", strconv.Itoa(len(out)))
	w.WriteHeader(status)
	_, _ = w.Write(out)
}
