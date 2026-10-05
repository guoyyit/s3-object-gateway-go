package server

import (
	"bytes"
	"encoding/xml"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/guoyyit/s3-object-gateway-go/internal/auth"
	"github.com/guoyyit/s3-object-gateway-go/internal/storage"
)

func startServerOnDir(t *testing.T, dir string) (*storage.Store, *httptest.Server) {
	t.Helper()
	store, err := storage.Open(t.Context(), dir)
	if err != nil {
		t.Fatal(err)
	}
	srv := New(Config{
		Credentials: []auth.Credentials{{AccessKeyID: testAccessKey, SecretAccessKey: testSecretKey}},
	}, store)
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(func() {
		ts.Close()
		store.Close()
	})
	return store, ts
}

func initiateUpload(t *testing.T, ts *httptest.Server, bucket, key string) string {
	t.Helper()
	resp := doSigned(t, ts, http.MethodPut, "/"+bucket+"/"+key+"?uploads", nil, nil)
	if resp.StatusCode != http.StatusOK {
		data, _ := io.ReadAll(resp.Body)
		t.Fatalf("initiate: %d %s", resp.StatusCode, data)
	}
	var out struct {
		UploadID string `xml:"UploadId"`
	}
	decodeXML(t, resp.Body, &out)
	return out.UploadID
}

func uploadTestPart(t *testing.T, ts *httptest.Server, bucket, key, uploadID string, n int, body []byte) string {
	t.Helper()
	path := "/" + bucket + "/" + key + "?partNumber=" + itoa(n) + "&uploadId=" + uploadID
	resp := doSigned(t, ts, http.MethodPut, path, body, nil)
	if resp.StatusCode != http.StatusOK {
		data, _ := io.ReadAll(resp.Body)
		t.Fatalf("upload part: %d %s", resp.StatusCode, data)
	}
	etag := strings.Trim(resp.Header.Get("ETag"), "\"")
	resp.Body.Close()
	return etag
}

func completeUpload(t *testing.T, ts *httptest.Server, bucket, key, uploadID string, parts map[int]string) {
	t.Helper()
	var sb strings.Builder
	sb.WriteString("<CompleteMultipartUpload>")
	for n := 1; n <= len(parts); n++ {
		sb.WriteString("<Part><PartNumber>")
		sb.WriteString(itoa(n))
		sb.WriteString("</PartNumber><ETag>\"")
		sb.WriteString(parts[n])
		sb.WriteString("\"</ETag></Part>")
	}
	sb.WriteString("</CompleteMultipartUpload>")
	path := "/" + bucket + "/" + key + "?uploadId=" + uploadID
	resp := doSigned(t, ts, http.MethodPost, path, []byte(sb.String()),
		map[string]string{"Content-Type": "application/xml"})
	if resp.StatusCode != http.StatusOK {
		data, _ := io.ReadAll(resp.Body)
		t.Fatalf("complete: %d %s", resp.StatusCode, data)
	}
	resp.Body.Close()
}

func decodeXML(t *testing.T, body io.Reader, v interface{}) {
	t.Helper()
	data, err := io.ReadAll(body)
	if err != nil {
		t.Fatal(err)
	}
	if err := xml.Unmarshal(data, v); err != nil {
		t.Fatalf("xml %q: %v", data, err)
	}
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b []byte
	for n > 0 {
		b = append([]byte{byte('0' + n%10)}, b...)
		n /= 10
	}
	return string(b)
}

func TestMultipartLifecycle(t *testing.T) {
	_, ts := newTestServer(t)
	createTestBucket(t, ts, "mp")

	uploadID := initiateUpload(t, ts, "mp", "large.bin")

	part1 := bytes.Repeat([]byte("A"), 5*1024*1024)
	part2 := []byte("tail")
	e1 := uploadTestPart(t, ts, "mp", "large.bin", uploadID, 1, part1)
	e2 := uploadTestPart(t, ts, "mp", "large.bin", uploadID, 2, part2)

	completeUpload(t, ts, "mp", "large.bin", uploadID, map[int]string{1: e1, 2: e2})

	resp := doSigned(t, ts, http.MethodGet, "/mp/large.bin", nil, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("get assembled: %d", resp.StatusCode)
	}
	data, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	want := append(part1, part2...)
	if !bytes.Equal(data, want) {
		t.Fatalf("assembled size %d want %d", len(data), len(want))
	}
	if !strings.HasPrefix(resp.Header.Get("ETag"), "\"") || !strings.Contains(resp.Header.Get("ETag"), "-2\"") {
		t.Fatalf("multipart etag %q", resp.Header.Get("ETag"))
	}

	// Completed upload no longer appears in multipart listing.
	resp = doSigned(t, ts, http.MethodGet, "/mp?uploads", nil, nil)
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if strings.Contains(string(body), uploadID) {
		t.Fatalf("completed upload still listed: %s", body)
	}
}

func TestMultipartSmallPartRejected(t *testing.T) {
	_, ts := newTestServer(t)
	createTestBucket(t, ts, "mp")
	uploadID := initiateUpload(t, ts, "mp", "x")
	e1 := uploadTestPart(t, ts, "mp", "x", uploadID, 1, []byte("small"))
	e2 := uploadTestPart(t, ts, "mp", "x", uploadID, 2, []byte("small2"))

	var sb strings.Builder
	sb.WriteString("<CompleteMultipartUpload>")
	sb.WriteString("<Part><PartNumber>1</PartNumber><ETag>\"" + e1 + "\"</ETag></Part>")
	sb.WriteString("<Part><PartNumber>2</PartNumber><ETag>\"" + e2 + "\"</ETag></Part>")
	sb.WriteString("</CompleteMultipartUpload>")
	resp := doSigned(t, ts, http.MethodPost, "/mp/x?uploadId="+uploadID,
		[]byte(sb.String()), map[string]string{"Content-Type": "application/xml"})
	if resp.StatusCode != http.StatusBadRequest {
		data, _ := io.ReadAll(resp.Body)
		t.Fatalf("expected 400 at complete, got %d %s", resp.StatusCode, data)
	}
	assertBodyContains(t, resp.Body, "EntityTooSmall")
}

func TestMultipartWrongETagRejected(t *testing.T) {
	_, ts := newTestServer(t)
	createTestBucket(t, ts, "mp")
	uploadID := initiateUpload(t, ts, "mp", "w")
	e1 := uploadTestPart(t, ts, "mp", "w", uploadID, 1, []byte("abc"))

	body := "<CompleteMultipartUpload><Part><PartNumber>1</PartNumber>" +
		"<ETag>\"" + e1 + "dead\"</ETag></Part></CompleteMultipartUpload>"
	resp := doSigned(t, ts, http.MethodPost, "/mp/w?uploadId="+uploadID,
		[]byte(body), map[string]string{"Content-Type": "application/xml"})
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", resp.StatusCode)
	}
	assertBodyContains(t, resp.Body, "InvalidPart")
}

func TestMultipartAbortInvisible(t *testing.T) {
	_, ts := newTestServer(t)
	createTestBucket(t, ts, "mp")
	uploadID := initiateUpload(t, ts, "mp", "secret")

	// While in progress, key must not be readable.
	resp := doSigned(t, ts, http.MethodGet, "/mp/secret", nil, nil)
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("in-progress object readable: %d", resp.StatusCode)
	}
	resp.Body.Close()

	resp = doSigned(t, ts, http.MethodGet, "/mp?list-type=2", nil, nil)
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if strings.Contains(string(body), "<Key>secret</Key>") {
		t.Fatalf("in-progress key listed as object")
	}

	resp = doSigned(t, ts, http.MethodDelete, "/mp/secret?uploadId="+uploadID, nil, nil)
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("abort: %d", resp.StatusCode)
	}
	resp.Body.Close()

	// Aborted upload cannot be resumed.
	bigPart := bytes.Repeat([]byte("B"), 5*1024*1024)
	path := "/mp/secret?partNumber=1&uploadId=" + uploadID
	resp = doSigned(t, ts, http.MethodPut, path, bigPart, nil)
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("expected 404 after abort, got %d", resp.StatusCode)
	}
	resp.Body.Close()
}

func TestMultipartResumableAcrossRestart(t *testing.T) {
	dir := t.TempDir()
	_, ts := startServerOnDir(t, dir)
	createTestBucket(t, ts, "mp")
	uploadID := initiateUpload(t, ts, "mp", "resume")
	bigPart := bytes.Repeat([]byte("C"), 5*1024*1024)
	e1 := uploadTestPart(t, ts, "mp", "resume", uploadID, 1, bigPart)
	ts.Close()

	// Restart: upload state must be recognized and listable.
	_, ts2 := startServerOnDir(t, dir)
	resp := doSigned(t, ts2, http.MethodGet, "/mp?uploads", nil, nil)
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if !strings.Contains(string(body), uploadID) {
		t.Fatalf("upload not recovered after restart: %s", body)
	}

	e2 := uploadTestPart(t, ts2, "mp", "resume", uploadID, 2, []byte("end"))
	completeUpload(t, ts2, "mp", "resume", uploadID, map[int]string{1: e1, 2: e2})

	resp = doSigned(t, ts2, http.MethodGet, "/mp/resume", nil, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("final get: %d", resp.StatusCode)
	}
	data, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if len(data) != 5*1024*1024+3 {
		t.Fatalf("assembled size %d", len(data))
	}
}
