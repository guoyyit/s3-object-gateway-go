// Package api implements the S3-compatible HTTP layer.
package api

import (
	"errors"
	"net/http"

	"github.com/guoyyit/s3-object-gateway-go/internal/auth"
	s3err "github.com/guoyyit/s3-object-gateway-go/internal/s3error"
	"github.com/guoyyit/s3-object-gateway-go/internal/storage"
)

// Server wires the store and SigV4 verifier to HTTP handlers.
type Server struct {
	store    *storage.Store
	verifier *auth.Verifier
	mux      *http.ServeMux
}

// NewServer constructs an API server.
func NewServer(store *storage.Store, verifier *auth.Verifier) *Server {
	s := &Server{store: store, verifier: verifier, mux: http.NewServeMux()}
	s.mux.HandleFunc("/", s.route)
	return s
}

// Handler returns the http.Handler for the server.
func (s *Server) Handler() http.Handler {
	return s.mux
}

// ServeHTTP is retained for interface compatibility; the mux is the handler.
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s.mux.ServeHTTP(w, r)
}

// route applies authentication then dispatches to the resource handler.
func (s *Server) route(w http.ResponseWriter, r *http.Request) {
	requestID := s3err.RequestID()
	w.Header().Set("X-Amz-Request-Id", requestID)

	if err := s.verifier.Verify(r); err != nil {
		writeS3Error(w, r, requestID, mapAuthError(err))
		return
	}

	key, action := parseResource(r)
	q := r.URL.Query()

	switch {
	case key == "" && action == "":
		switch r.Method {
		case http.MethodGet:
			if q.Get("list-type") != "" || q.Has("prefix") || q.Has("marker") || q.Has("max-keys") {
				s.handleListObjects(w, r)
				return
			}
			s.handleListBuckets(w, r)
		case http.MethodHead:
			s.handleBucketHead(w, r)
		case http.MethodPost:
			if r.URL.Query().Get("delete") != "" {
				s.handleBatchDelete(w, r)
				return
			}
			s.respondError(w, r, s3err.New("MethodNotAllowed"))
		default:
			s.respondError(w, r, s3err.New("MethodNotAllowed"))
		}
		return

	case key == "" && action != "":
		s.handleBucketAction(w, r, action)
		return

	case key != "" && action == "":
		s.handleObject(w, r, key)
		return

	case key != "" && action != "":
		s.handleObjectAction(w, r, key, action)
		return
	}
}

func mapAuthError(err error) *s3err.Error {
	switch {
	case errors.Is(err, auth.ErrBodySignature):
		return s3err.New("SignatureDoesNotMatch")
	case errors.Is(err, auth.ErrInvalidKey):
		return s3err.New("InvalidAccessKeyId")
	case errors.Is(err, auth.ErrTimeSkew):
		return s3err.New("RequestTimeTooSkewed")
	case errors.Is(err, auth.ErrSignature):
		return s3err.New("SignatureDoesNotMatch")
	default:
		return s3err.New("InternalError")
	}
}

func writeS3Error(w http.ResponseWriter, r *http.Request, requestID string, e *s3err.Error) {
	e.RequestID = requestID
	s3err.WriteXML(w, r, e)
}

// parseResource extracts the object key and the query-string sub-resource
// action (uploads, uploadId, etc.).
func parseResource(r *http.Request) (key, action string) {
	key = r.URL.Path
	if len(key) > 0 && key[0] == '/' {
		key = key[1:]
	}
	q := r.URL.Query()
	switch {
	case q.Has("uploads"):
		if key == "" {
			action = "uploads"
		} else {
			action = "initiate-upload"
		}
	case q.Get("uploadId") != "":
		action = "uploadpart"
	case q.Has("acl") || q.Has("versioning") || q.Has("versions") ||
		q.Has("lifecycle") || q.Has("cors") || q.Has("policy") ||
		q.Has("website") || q.Has("tagging") || q.Has("encryption") ||
		q.Has("replication") || q.Has("inventory") || q.Has("analytics") ||
		q.Has("accelerate") || q.Has("requestPayment") || q.Has("metrics") ||
		q.Has("ownershipControls") || q.Has("object-lock") || q.Has("legal-hold") ||
		q.Has("retention") || q.Has("restore") || q.Has("select") ||
		q.Has("object-lock-configuration"):
		action = "unsupported"
	}
	return key, action
}
