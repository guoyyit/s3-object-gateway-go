package server

import (
	"bytes"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func doSigned(t *testing.T, ts *httptest.Server, method, path string, body []byte, headers map[string]string) *http.Response {
	t.Helper()
	req := newSignedRequest(method, ts.URL+path, body, time.Now())
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	return resp
}

func createTestBucket(t *testing.T, ts *httptest.Server, bucket string) {
	t.Helper()
	resp := doSigned(t, ts, http.MethodPut, "/"+bucket, nil, nil)
	if resp.StatusCode != http.StatusOK {
		data, _ := io.ReadAll(resp.Body)
		t.Fatalf("create bucket: %d %s", resp.StatusCode, data)
	}
	resp.Body.Close()
}

func TestPutGetDeleteObject(t *testing.T) {
	_, ts := newTestServer(t)
	createTestBucket(t, ts, "bkt")

	payload := []byte("hello world object body")
	resp := doSigned(t, ts, http.MethodPut, "/bkt/obj1", payload, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("put status %d", resp.StatusCode)
	}
	resp.Body.Close()

	resp = doSigned(t, ts, http.MethodGet, "/bkt/obj1", nil, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("get status %d", resp.StatusCode)
	}
	data, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if !bytes.Equal(data, payload) {
		t.Fatalf("body mismatch: %q", data)
	}

	resp = doSigned(t, ts, http.MethodHead, "/bkt/obj1", nil, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("head status %d", resp.StatusCode)
	}
	if resp.Header.Get("Content-Length") != "23" {
		t.Fatalf("content-length %q", resp.Header.Get("Content-Length"))
	}
	resp.Body.Close()

	resp = doSigned(t, ts, http.MethodDelete, "/bkt/obj1", nil, nil)
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("delete status %d", resp.StatusCode)
	}
	resp.Body.Close()

	resp = doSigned(t, ts, http.MethodGet, "/bkt/obj1", nil, nil)
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("expected 404 after delete, got %d", resp.StatusCode)
	}
	resp.Body.Close()
}

func TestRangeReads(t *testing.T) {
	_, ts := newTestServer(t)
	createTestBucket(t, ts, "bkt")
	payload := []byte("0123456789")
	resp := doSigned(t, ts, http.MethodPut, "/bkt/r", payload, nil)
	resp.Body.Close()

	cases := []struct {
		rangeHeader string
		wantStatus  int
		wantBody    string
		wantCR      string
	}{
		{"bytes=2-5", http.StatusPartialContent, "2345", "bytes 2-5/10"},
		{"bytes=7-", http.StatusPartialContent, "789", "bytes 7-9/10"},
		{"bytes=-3", http.StatusPartialContent, "789", "bytes 7-9/10"},
		{"bytes=0-100", http.StatusPartialContent, "0123456789", "bytes 0-9/10"},
	}
	for _, tc := range cases {
		resp = doSigned(t, ts, http.MethodGet, "/bkt/r", nil, map[string]string{"Range": tc.rangeHeader})
		if resp.StatusCode != tc.wantStatus {
			t.Fatalf("%s: status %d", tc.rangeHeader, resp.StatusCode)
		}
		data, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if string(data) != tc.wantBody {
			t.Fatalf("%s: body %q want %q", tc.rangeHeader, data, tc.wantBody)
		}
		if cr := resp.Header.Get("Content-Range"); cr != tc.wantCR {
			t.Fatalf("%s: content-range %q want %q", tc.rangeHeader, cr, tc.wantCR)
		}
	}

	resp = doSigned(t, ts, http.MethodGet, "/bkt/r", nil, map[string]string{"Range": "bytes=100-200"})
	if resp.StatusCode != http.StatusRequestedRangeNotSatisfiable {
		t.Fatalf("expected 416, got %d", resp.StatusCode)
	}
	resp.Body.Close()
}

func TestUserMetadataRoundTrip(t *testing.T) {
	_, ts := newTestServer(t)
	createTestBucket(t, ts, "bkt")
	resp := doSigned(t, ts, http.MethodPut, "/bkt/meta", []byte("data"),
		map[string]string{
			"Content-Type":    "application/json",
			"X-Amz-Meta-Team": "core",
		})
	resp.Body.Close()

	resp = doSigned(t, ts, http.MethodHead, "/bkt/meta", nil, nil)
	if resp.Header.Get("Content-Type") != "application/json" {
		t.Fatalf("content-type %q", resp.Header.Get("Content-Type"))
	}
	if resp.Header.Get("X-Amz-Meta-Team") != "core" {
		t.Fatalf("metadata %q", resp.Header.Get("X-Amz-Meta-Team"))
	}
	resp.Body.Close()
}

func TestConditionalRequests(t *testing.T) {
	_, ts := newTestServer(t)
	createTestBucket(t, ts, "bkt")
	resp := doSigned(t, ts, http.MethodPut, "/bkt/c", []byte("abc"), nil)
	etag := resp.Header.Get("ETag")
	resp.Body.Close()

	resp = doSigned(t, ts, http.MethodGet, "/bkt/c", nil, map[string]string{"If-Match": etag})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("if-match same etag: %d", resp.StatusCode)
	}
	resp.Body.Close()

	resp = doSigned(t, ts, http.MethodGet, "/bkt/c", nil, map[string]string{"If-Match": `"deadbeef"`})
	if resp.StatusCode != http.StatusPreconditionFailed {
		t.Fatalf("if-match wrong etag: %d", resp.StatusCode)
	}
	resp.Body.Close()

	resp = doSigned(t, ts, http.MethodGet, "/bkt/c", nil, map[string]string{"If-None-Match": etag})
	if resp.StatusCode != http.StatusNotModified {
		t.Fatalf("if-none-match: %d", resp.StatusCode)
	}
	resp.Body.Close()
}

func TestNoSuchBucketAndKey(t *testing.T) {
	_, ts := newTestServer(t)
	resp := doSigned(t, ts, http.MethodGet, "/missing/key", nil, nil)
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("missing bucket: %d", resp.StatusCode)
	}
	resp.Body.Close()

	createTestBucket(t, ts, "b")
	resp = doSigned(t, ts, http.MethodGet, "/b/nope", nil, nil)
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("missing key: %d", resp.StatusCode)
	}
	resp.Body.Close()
}

func TestContentMD5Verification(t *testing.T) {
	_, ts := newTestServer(t)
	createTestBucket(t, ts, "b")

	// Correct base64 MD5 of "abc".
	resp := doSigned(t, ts, http.MethodPut, "/b/md5ok", []byte("abc"),
		map[string]string{"Content-MD5": "kAFQmDzST7DWlj99KOF/cg=="})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("valid md5: %d", resp.StatusCode)
	}
	resp.Body.Close()

	resp = doSigned(t, ts, http.MethodPut, "/b/md5bad", []byte("abc"),
		map[string]string{"Content-MD5": "AAAAAAAAAAAAAAAAAAAAAA=="})
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("mismatched md5: %d", resp.StatusCode)
	}
	assertBodyContains(t, resp.Body, "BadDigest")

	resp = doSigned(t, ts, http.MethodPut, "/b/md5invalid", []byte("abc"),
		map[string]string{"Content-MD5": "not-base64!!"})
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("invalid md5: %d", resp.StatusCode)
	}
	resp.Body.Close()
}
