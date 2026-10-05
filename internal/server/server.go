package server

import (
	"crypto/rand"
	"encoding/hex"
	"net/http"
	"strings"
	"time"

	"github.com/guoyyit/s3-object-gateway-go/internal/auth"
	"github.com/guoyyit/s3-object-gateway-go/internal/protocol"
	"github.com/guoyyit/s3-object-gateway-go/internal/storage"
)

// Config configures the HTTP server.
type Config struct {
	Addr        string
	Credentials []auth.Credentials
	DataDir     string
	MaxSkew     time.Duration
}

// Server is the S3-compatible HTTP gateway.
type Server struct {
	cfg      Config
	store    *storage.Store
	verifier *auth.Verifier
}

// New constructs a server over an opened store.
func New(cfg Config, store *storage.Store) *Server {
	credStore := auth.NewStore(cfg.Credentials)
	return &Server{
		cfg:      cfg,
		store:    store,
		verifier: auth.NewVerifier(credStore, time.Now),
	}
}

// Handler returns the root http.Handler with authentication applied.
func (s *Server) Handler() http.Handler {
	return http.HandlerFunc(s.authenticate)
}

func (s *Server) authenticate(w http.ResponseWriter, r *http.Request) {
	id, err := s.verifier.Verify(r)
	requestID := newRequestID()
	w.Header().Set("X-Amz-Request-Id", requestID)
	if err != nil {
		protocol.WriteError(w, r, err, requestID)
		return
	}
	ctx := auth.WithIdentity(r.Context(), id)
	s.route(w, r.WithContext(ctx), requestID)
}

func newRequestID() string {
	var b [8]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}

func (s *Server) route(w http.ResponseWriter, r *http.Request, requestID string) {
	bucket, key, ok := splitResource(r)
	if !ok {
		s.handleListBuckets(w, r, requestID)
		return
	}

	q := r.URL.Query()

	if key == "" {
		switch {
		case q.Get("location") != "":
			s.handleBucketLocation(w, r, bucket, requestID)
		case q.Has("uploads"):
			s.handleListMultipart(w, r, bucket, requestID)
		case q.Get("list-type") == "2":
			s.handleListObjects(w, r, bucket, requestID)
		default:
			if r.Method == http.MethodGet {
				s.handleListObjectsV1(w, r, bucket, requestID)
			} else {
				s.handleBucket(w, r, bucket, requestID)
			}
		}
		return
	}

	switch {
	case r.Method == http.MethodDelete && q.Has("uploadId"):
		s.handleMultipartLifecycle(w, r, bucket, key, requestID)
		return
	case r.Method == http.MethodPost && q.Has("uploadId"):
		s.handleMultipartLifecycle(w, r, bucket, key, requestID)
		return
	case q.Has("uploadId"):
		s.handleUploadPartOrList(w, r, bucket, key, requestID)
		return
	case q.Has("uploads"):
		s.handleMultipartLifecycle(w, r, bucket, key, requestID)
		return
	}

	if r.Header.Get("X-Amz-Copy-Source") != "" && r.Method == http.MethodPut {
		s.handleCopyObject(w, r, bucket, key, requestID)
		return
	}

	switch r.Method {
	case http.MethodPut:
		s.handlePutObject(w, r, bucket, key, requestID)
	case http.MethodGet:
		s.handleGetObject(w, r, bucket, key, requestID)
	case http.MethodHead:
		s.handleHeadObject(w, r, bucket, key, requestID)
	case http.MethodDelete:
		s.handleDeleteObject(w, r, bucket, key, requestID)
	default:
		protocol.WriteError(w, r, protocol.ErrNotImplemented, requestID)
	}
}

// splitResource returns the bucket and key from the URL path.
func splitResource(r *http.Request) (bucket, key string, ok bool) {
	path := strings.TrimPrefix(r.URL.Path, "/")
	if path == "" {
		return "", "", false
	}
	parts := strings.SplitN(path, "/", 2)
	bucket = parts[0]
	if len(parts) == 2 {
		key = parts[1]
	}
	return bucket, key, true
}
