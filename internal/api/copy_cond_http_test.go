package api

import (
	"net/http"
	"testing"
)

func TestHTTPConditionalAndCopy(t *testing.T) {
	c := newTestServer(t)
	resp := c.req("PUT", "/src", []byte("copy-source-data"), http.Header{"Content-Type": {"text/plain"}})
	resp.Body.Close()

	head := c.req("HEAD", "/src", nil, nil)
	etag := head.Header.Get("ETag")
	head.Body.Close()

	hdr := http.Header{"If-Match": {`"deadbeef"`}}
	resp = c.req("GET", "/src", nil, hdr)
	if resp.StatusCode != 412 {
		t.Fatalf("if-match mismatch %d", resp.StatusCode)
	}
	resp.Body.Close()

	hdr = http.Header{"If-Match": {etag}}
	resp = c.req("GET", "/src", nil, hdr)
	if resp.StatusCode != 200 {
		t.Fatalf("if-match ok %d", resp.StatusCode)
	}
	resp.Body.Close()

	// If-None-Match hit -> 304.
	hdr = http.Header{"If-None-Match": {etag}}
	resp = c.req("GET", "/src", nil, hdr)
	if resp.StatusCode != 304 {
		t.Fatalf("if-none-match %d", resp.StatusCode)
	}
	resp.Body.Close()

	hdr = http.Header{"X-Amz-Copy-Source": {"/local/src"}}
	resp = c.req("PUT", "/dst", nil, hdr)
	if resp.StatusCode != 200 {
		t.Fatalf("copy status %d", resp.StatusCode)
	}
	resp.Body.Close()

	resp = c.req("GET", "/dst", nil, nil)
	if string(readBody(t, resp)) != "copy-source-data" {
		t.Fatal("copy content mismatch")
	}

	// Copy source condition failure -> 412.
	hdr = http.Header{
		"X-Amz-Copy-Source":          {"/local/src"},
		"X-Amz-Copy-Source-If-Match": {`"nope"`},
	}
	resp = c.req("PUT", "/dst2", nil, hdr)
	if resp.StatusCode != 412 {
		t.Fatalf("copy cond %d", resp.StatusCode)
	}
	resp.Body.Close()

	hdr = http.Header{"X-Amz-Copy-Source": {"/local/missing"}}
	resp = c.req("PUT", "/dst3", nil, hdr)
	if resp.StatusCode != 404 {
		t.Fatalf("copy missing %d", resp.StatusCode)
	}
	resp.Body.Close()
}

func TestHTTPUnsupportedFeature(t *testing.T) {
	c := newTestServer(t)
	hdr := http.Header{"X-Amz-Server-Side-Encryption": {"AES256"}}
	resp := c.req("PUT", "/enc", []byte("x"), hdr)
	if resp.StatusCode != 501 {
		t.Fatalf("sse status %d", resp.StatusCode)
	}
	resp.Body.Close()
}
