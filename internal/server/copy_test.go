package server

import (
	"io"
	"net/http"
	"testing"
)

func TestCopyObject(t *testing.T) {
	_, ts := newTestServer(t)
	createTestBucket(t, ts, "src")
	createTestBucket(t, ts, "dst")

	resp := doSigned(t, ts, http.MethodPut, "/src/orig", []byte("copy me"), nil)
	etag := resp.Header.Get("ETag")
	resp.Body.Close()

	resp = doSigned(t, ts, http.MethodPut, "/dst/copy", nil, map[string]string{
		"X-Amz-Copy-Source": "/src/orig",
	})
	if resp.StatusCode != http.StatusOK {
		data, _ := io.ReadAll(resp.Body)
		t.Fatalf("copy: %d %s", resp.StatusCode, data)
	}
	resp.Body.Close()

	resp = doSigned(t, ts, http.MethodGet, "/dst/copy", nil, nil)
	data, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if string(data) != "copy me" {
		t.Fatalf("copied body %q", data)
	}

	// Conditional copy with wrong etag must fail precondition.
	resp = doSigned(t, ts, http.MethodPut, "/dst/copy2", nil, map[string]string{
		"X-Amz-Copy-Source":          "/src/orig",
		"X-Amz-Copy-Source-If-Match": `"00000000000000000000000000000000"`,
	})
	if resp.StatusCode != http.StatusPreconditionFailed {
		t.Fatalf("expected 412, got %d", resp.StatusCode)
	}
	resp.Body.Close()

	// Matching condition copies successfully.
	resp = doSigned(t, ts, http.MethodPut, "/dst/copy3", nil, map[string]string{
		"X-Amz-Copy-Source":          "/src/orig",
		"X-Amz-Copy-Source-If-Match": etag,
	})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("conditional copy: %d", resp.StatusCode)
	}
	resp.Body.Close()

	// Copying a missing source must fail with 404.
	resp = doSigned(t, ts, http.MethodPut, "/dst/copy4", nil, map[string]string{
		"X-Amz-Copy-Source": "/src/missing",
	})
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("missing source copy: %d", resp.StatusCode)
	}
	resp.Body.Close()
}
