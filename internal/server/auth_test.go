package server

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/guoyyit/s3-object-gateway-go/internal/auth"
	"github.com/guoyyit/s3-object-gateway-go/internal/storage"
)

func newTestServer(t *testing.T) (*Server, *httptest.Server) {
	t.Helper()
	dir := t.TempDir()
	store, err := storage.Open(t.Context(), dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })

	srv := New(Config{
		Credentials: []auth.Credentials{{AccessKeyID: testAccessKey, SecretAccessKey: testSecretKey}},
	}, store)
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)
	return srv, ts
}

func TestSigV4Valid(t *testing.T) {
	_, ts := newTestServer(t)
	req := newSignedRequest(http.MethodGet, ts.URL+"/", nil, time.Now())
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
}

func TestSigV4BadSignature(t *testing.T) {
	_, ts := newTestServer(t)
	req := newSignedRequest(http.MethodGet, ts.URL+"/", nil, time.Now())
	authz := req.Header.Get("Authorization")
	replacement := "1"
	if authz[len(authz)-1] == '1' {
		replacement = "0"
	}
	tampered := authz[:len(authz)-1] + replacement
	req.Header.Set("Authorization", tampered)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("expected 403, got %d", resp.StatusCode)
	}
	assertBodyContains(t, resp.Body, "SignatureDoesNotMatch")
}

func TestSigV4InvalidAccessKey(t *testing.T) {
	_, ts := newTestServer(t)
	req := newSignedRequest(http.MethodGet, ts.URL+"/", nil, time.Now())
	authz := req.Header.Get("Authorization")
	authz = strings.Replace(authz, testAccessKey, "AKIAUNKNOWN", 1)
	req.Header.Set("Authorization", authz)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("expected 403, got %d", resp.StatusCode)
	}
	assertBodyContains(t, resp.Body, "InvalidAccessKeyId")
}

func TestSigV4SkewTooLarge(t *testing.T) {
	_, ts := newTestServer(t)
	req := newSignedRequest(http.MethodGet, ts.URL+"/", nil, time.Now().Add(-16*time.Minute))
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("expected 403, got %d", resp.StatusCode)
	}
	assertBodyContains(t, resp.Body, "RequestTimeTooSkewed")
}

func TestSigV4MissingAuth(t *testing.T) {
	_, ts := newTestServer(t)
	resp, err := http.Get(ts.URL + "/")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", resp.StatusCode)
	}
}

func assertBodyContains(t *testing.T, body io.Reader, want string) {
	t.Helper()
	data, err := io.ReadAll(body)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), want) {
		t.Fatalf("response body %q does not contain %q", string(data), want)
	}
}

func TestUnsupportedOperationReturns501(t *testing.T) {
	_, ts := newTestServer(t)
	createTestBucket(t, ts, "b")
	// POST /b/key without uploads/uploadId is not a supported operation.
	resp := doSigned(t, ts, http.MethodPost, "/b/key", nil, nil)
	if resp.StatusCode != http.StatusNotImplemented {
		t.Fatalf("expected 501, got %d", resp.StatusCode)
	}
	assertBodyContains(t, resp.Body, "NotImplemented")
}
