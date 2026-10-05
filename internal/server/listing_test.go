package server

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func listV2In(t *testing.T, ts *httptest.Server, bucket, query string) string {
	t.Helper()
	resp := doSigned(t, ts, http.MethodGet, "/"+bucket+"?list-type=2&"+query, nil, nil)
	if resp.StatusCode != http.StatusOK {
		data, _ := io.ReadAll(resp.Body)
		t.Fatalf("list %s: %d %s", query, resp.StatusCode, data)
	}
	data, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	return string(data)
}

func extractContinuationToken(body string) string {
	marker := "<NextContinuationToken>"
	i := strings.Index(body, marker)
	if i < 0 {
		return ""
	}
	rest := body[i+len(marker):]
	j := strings.Index(rest, "</NextContinuationToken>")
	return rest[:j]
}

func TestListObjectsPrefixDelimiterPaging(t *testing.T) {
	_, ts := newTestServer(t)
	createTestBucket(t, ts, "lb")

	keys := []string{
		"a.txt",
		"photos/one.jpg",
		"photos/two.jpg",
		"photos/2024/three.jpg",
		"photos/2025/four.jpg",
		"docs/readme.md",
	}
	for _, key := range keys {
		resp := doSigned(t, ts, http.MethodPut, "/lb/"+key, []byte("x"), nil)
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("put %s: %d", key, resp.StatusCode)
		}
		resp.Body.Close()
	}

	body := listV2In(t, ts, "lb", "max-keys=3")
	count := strings.Count(body, "<Key>")
	if count != 3 {
		t.Fatalf("page1 key count %d", count)
	}
	if !strings.Contains(body, "<IsTruncated>true</IsTruncated>") {
		t.Fatalf("expected truncated: %s", body)
	}
	token := extractContinuationToken(body)
	if token == "" {
		t.Fatal("missing continuation token")
	}

	seenKeys := map[string]bool{}
	for _, line := range extractKeys(body) {
		seenKeys[line] = true
	}
	body2 := listV2In(t, ts, "lb", "max-keys=3&continuation-token="+token)
	for _, line := range extractKeys(body2) {
		if seenKeys[line] {
			t.Fatalf("key %q duplicated across pages", line)
		}
		seenKeys[line] = true
	}
	token2 := extractContinuationToken(body2)
	body3 := listV2In(t, ts, "lb", "max-keys=100&continuation-token="+token2)
	for _, line := range extractKeys(body3) {
		seenKeys[line] = true
	}
	for _, key := range keys {
		if !seenKeys[key] {
			t.Fatalf("key %q missing across pages", key)
		}
	}

	// Prefix + delimiter rolls up to CommonPrefixes.
	rolled := listV2In(t, ts, "lb", "prefix=photos/&delimiter=/")
	if !strings.Contains(rolled, "<Prefix>photos/2024/</Prefix>") {
		t.Fatalf("missing common prefix 2024: %s", rolled)
	}
	if !strings.Contains(rolled, "<Prefix>photos/2025/</Prefix>") {
		t.Fatalf("missing common prefix 2025: %s", rolled)
	}
	if strings.Count(rolled, "<Key>") != 2 {
		t.Fatalf("rolled-up listing should expose only 2 direct objects: %s", rolled)
	}

	// Plain prefix filter.
	prefixed := listV2In(t, ts, "lb", "prefix=docs/")
	if !strings.Contains(prefixed, "<Key>docs/readme.md</Key>") {
		t.Fatalf("prefix filter wrong: %s", prefixed)
	}
}

func extractKeys(body string) []string {
	var out []string
	for {
		i := strings.Index(body, "<Key>")
		if i < 0 {
			return out
		}
		rest := body[i+len("<Key>"):]
		j := strings.Index(rest, "</Key>")
		out = append(out, rest[:j])
		body = rest[j:]
	}
}

func TestListEmptyBucket(t *testing.T) {
	_, ts := newTestServer(t)
	createTestBucket(t, ts, "empty")
	body := listV2In(t, ts, "empty", "")
	if !strings.Contains(body, "<KeyCount>0</KeyCount>") {
		t.Fatalf("expected empty listing: %s", body)
	}
}

func TestListObjectsV1(t *testing.T) {
	_, ts := newTestServer(t)
	createTestBucket(t, ts, "v1")
	resp := doSigned(t, ts, http.MethodPut, "/v1/a", []byte("x"), nil)
	resp.Body.Close()
	resp = doSigned(t, ts, http.MethodGet, "/v1?prefix=a", nil, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("v1 list: %d", resp.StatusCode)
	}
	data, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if !strings.Contains(string(data), "ListBucketResult") || !strings.Contains(string(data), "<Key>a</Key>") {
		t.Fatalf("v1 response wrong: %s", data)
	}

	resp = doSigned(t, ts, http.MethodGet, "/v1?location", nil, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("location: %d", resp.StatusCode)
	}
	resp.Body.Close()
}
