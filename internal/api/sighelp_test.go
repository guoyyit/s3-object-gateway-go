package api

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/guoyyit/s3-object-gateway-go/internal/auth"
	"github.com/guoyyit/s3-object-gateway-go/internal/storage"
)

const (
	accessKey = "AKIATEST"
	secretKey = "supersecret"
	region    = "us-east-1"
)

type testClient struct {
	t      *testing.T
	base   string
	client *http.Client
	now    time.Time
	secret string
}

func newTestServer(t *testing.T) *testClient {
	t.Helper()
	store, err := storage.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })

	verifier := auth.NewVerifier(func(_ context.Context, k string) (string, bool, error) {
		if k == accessKey {
			return secretKey, true, nil
		}
		return "", false, nil
	})
	srv := httptest.NewServer(NewServer(store, verifier).Handler())
	t.Cleanup(srv.Close)

	return &testClient{
		t:      t,
		base:   srv.URL,
		client: srv.Client(),
		now:    time.Now().UTC(),
		secret: secretKey,
	}
}

func (c *testClient) req(method, path string, body []byte, hdr http.Header) *http.Response {
	c.t.Helper()
	var rdr io.Reader
	if body != nil {
		rdr = bytes.NewReader(body)
	}
	req, err := http.NewRequest(method, c.base+path, rdr)
	if err != nil {
		c.t.Fatal(err)
	}
	if hdr != nil {
		req.Header = hdr
	}
	c.sign(req, body)
	resp, err := c.client.Do(req)
	if err != nil {
		c.t.Fatal(err)
	}
	return resp
}

func (c *testClient) sign(r *http.Request, body []byte) {
	date := c.now.Format("20060102")
	amzDate := c.now.Format("20060102T150405Z")
	r.Header.Set("X-Amz-Date", amzDate)
	r.Host = strings.TrimPrefix(c.base, "http://")

	payloadHash := "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855"
	if body != nil {
		sum := sha256.Sum256(body)
		payloadHash = hex.EncodeToString(sum[:])
	}
	r.Header.Set("X-Amz-Content-Sha256", payloadHash)

	headers := map[string]string{
		"host":                 r.Host,
		"x-amz-content-sha256": payloadHash,
		"x-amz-date":           amzDate,
	}
	for k := range r.Header {
		lk := strings.ToLower(k)
		if lk == "host" || lk == "x-amz-date" || lk == "x-amz-content-sha256" {
			continue
		}
		headers[lk] = strings.Join(r.Header[http.CanonicalHeaderKey(k)], ",")
	}
	names := make([]string, 0, len(headers))
	for k := range headers {
		names = append(names, k)
	}
	sort.Strings(names)

	var ch strings.Builder
	for _, n := range names {
		ch.WriteString(n)
		ch.WriteByte(':')
		ch.WriteString(strings.Join(strings.Fields(headers[n]), " "))
		ch.WriteByte('\n')
	}

	canonical := strings.Join([]string{
		r.Method,
		r.URL.EscapedPath(),
		canonicalQuery(r),
		strings.TrimSuffix(ch.String(), "\n"),
		strings.Join(names, ";"),
		payloadHash,
	}, "\n")

	scope := date + "/" + region + "/s3/aws4_request"
	sts := strings.Join([]string{
		"AWS4-HMAC-SHA256",
		amzDate,
		scope,
		sha256Hex(canonical),
	}, "\n")

	k := hmacSHA([]byte("AWS4"+c.secret), []byte(date))
	k = hmacSHA(k, []byte(region))
	k = hmacSHA(k, []byte("s3"))
	k = hmacSHA(k, []byte("aws4_request"))
	sig := hex.EncodeToString(hmacSHA(k, []byte(sts)))

	r.Header.Set("Authorization", "AWS4-HMAC-SHA256 Credential="+accessKey+"/"+scope+
		", SignedHeaders="+strings.Join(names, ";")+", Signature="+sig)
}

func canonicalQuery(r *http.Request) string {
	q := r.URL.Query()
	keys := make([]string, 0, len(q))
	for k := range q {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var parts []string
	for _, k := range keys {
		parts = append(parts, awsEnc(k)+"="+awsEnc(q.Get(k)))
	}
	return strings.Join(parts, "&")
}

func awsEnc(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		c := s[i]
		if (c >= 'A' && c <= 'Z') || (c >= 'a' && c <= 'z') || (c >= '0' && c <= '9') ||
			c == '-' || c == '_' || c == '.' || c == '~' {
			b.WriteByte(c)
			continue
		}
		fmt.Fprintf(&b, "%%%02X", c)
	}
	return b.String()
}

func sha256Hex(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}

func hmacSHA(key, data []byte) []byte {
	m := hmac.New(sha256.New, key)
	m.Write(data)
	return m.Sum(nil)
}

func readBody(t *testing.T, resp *http.Response) []byte {
	t.Helper()
	b, err := io.ReadAll(resp.Body)
	resp.Body.Close()
	if err != nil {
		t.Fatal(err)
	}
	return b
}
