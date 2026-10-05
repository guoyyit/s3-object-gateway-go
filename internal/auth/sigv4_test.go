package auth

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"net/http"
	"sort"
	"strings"
	"testing"
	"time"
)

const (
	testAccessKey = "AKIATEST"
	testSecretKey = "supersecret"
	testRegion    = "us-east-1"
)

// signRequest is a minimal SigV4 signer used to generate valid requests.
func signRequest(t *testing.T, r *http.Request, body []byte, when time.Time, secretOverride string) {
	t.Helper()
	secret := testSecretKey
	if secretOverride != "" {
		secret = secretOverride
	}
	when = when.UTC()
	date := when.Format("20060102")
	amzDate := when.Format("20060102T150405Z")
	r.Header.Set("X-Amz-Date", amzDate)

	payloadHash := emptySHA256
	if body != nil {
		sum := sha256.Sum256(body)
		payloadHash = hex.EncodeToString(sum[:])
	}
	r.Header.Set("X-Amz-Content-Sha256", payloadHash)

	signedHeaderNames := []string{"host", "x-amz-content-sha256", "x-amz-date"}
	sort.Strings(signedHeaderNames)

	var ch strings.Builder
	for _, hn := range signedHeaderNames {
		var val string
		if hn == "host" {
			val = r.Host
		} else {
			val = r.Header.Get(http.CanonicalHeaderKey(hn))
		}
		ch.WriteString(hn)
		ch.WriteByte(':')
		ch.WriteString(strings.Join(strings.Fields(val), " "))
		ch.WriteByte('\n')
	}

	canonical := strings.Join([]string{
		r.Method,
		canonicalURITest(r),
		canonicalQueryTest(r),
		strings.TrimSuffix(ch.String(), "\n"),
		strings.Join(signedHeaderNames, ";"),
		payloadHash,
	}, "\n")

	scope := date + "/" + testRegion + "/s3/aws4_request"
	sts := strings.Join([]string{
		"AWS4-HMAC-SHA256",
		amzDate,
		scope,
		hexSHA256([]byte(canonical)),
	}, "\n")

	key := deriveSigningKey(secret, date, testRegion)
	sig := hexHMAC(key, []byte(sts))
	r.Header.Set("Authorization",
		"AWS4-HMAC-SHA256 Credential="+testAccessKey+"/"+scope+
			", SignedHeaders="+strings.Join(signedHeaderNames, ";")+
			", Signature="+sig)
}

func canonicalURITest(r *http.Request) string {
	var b strings.Builder
	for i, seg := range strings.Split(r.URL.Path, "/") {
		if i > 0 {
			b.WriteByte('/')
		}
		b.WriteString(awsEncode(seg))
	}
	if b.Len() == 0 {
		return "/"
	}
	return b.String()
}

func canonicalQueryTest(r *http.Request) string {
	q := r.URL.Query()
	keys := make([]string, 0, len(q))
	for k := range q {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var parts []string
	for _, k := range keys {
		parts = append(parts, awsEncode(k)+"="+awsEncode(q.Get(k)))
	}
	return strings.Join(parts, "&")
}

func newSignedRequest(method, url string, body []byte, when time.Time) *http.Request {
	var rdr io.Reader
	if body != nil {
		rdr = bytes.NewReader(body)
	}
	r, _ := http.NewRequest(method, url, rdr)
	r.Host = "localhost:9000"
	signRequest(&testing.T{}, r, body, when, "")
	return r
}

func lookupOK(_ context.Context, key string) (string, bool, error) {
	if key == testAccessKey {
		return testSecretKey, true, nil
	}
	return "", false, nil
}

func TestSigV4Valid(t *testing.T) {
	v := NewVerifier(lookupOK)
	r := newSignedRequest("GET", "http://localhost:9000/obj", nil, time.Now())
	if err := v.Verify(r); err != nil {
		t.Fatalf("expected valid signature, got %v", err)
	}
}

func TestSigV4TamperedSignature(t *testing.T) {
	v := NewVerifier(lookupOK)
	r := newSignedRequest("GET", "http://localhost:9000/obj", nil, time.Now())
	authz := r.Header.Get("Authorization")
	r.Header.Set("Authorization", authz[:len(authz)-4]+"dead")
	if err := v.Verify(r); err != ErrSignature {
		t.Fatalf("expected ErrSignature, got %v", err)
	}
}

func TestSigV4InvalidKey(t *testing.T) {
	v := NewVerifier(func(_ context.Context, _ string) (string, bool, error) {
		return "", false, nil
	})
	r := newSignedRequest("GET", "http://localhost:9000/obj", nil, time.Now())
	if err := v.Verify(r); err != ErrInvalidKey {
		t.Fatalf("expected ErrInvalidKey, got %v", err)
	}
}

func TestSigV4TimeSkew(t *testing.T) {
	v := NewVerifier(lookupOK)
	old := time.Now().Add(-time.Hour)
	r := newSignedRequest("GET", "http://localhost:9000/obj", nil, old)
	if err := v.Verify(r); err != ErrTimeSkew {
		t.Fatalf("expected ErrTimeSkew, got %v", err)
	}
}

func TestSigV4BodyHashMismatch(t *testing.T) {
	v := NewVerifier(lookupOK)
	body := []byte("hello world")
	r := newSignedRequest("PUT", "http://localhost:9000/obj", body, time.Now())
	// Swap body after signing.
	r.Body = io.NopCloser(strings.NewReader("tampered body here!!"))
	r.ContentLength = int64(len("tampered body here!!"))
	if err := v.Verify(r); err != nil {
		// Header signature must still validate; body check happens on read.
		t.Fatalf("header verify: %v", err)
	}
	if _, err := io.Copy(io.Discard, r.Body); err != ErrBodySignature {
		t.Fatalf("expected ErrBodySignature, got %v", err)
	}
}

func TestSigV4CorrectBodyHash(t *testing.T) {
	v := NewVerifier(lookupOK)
	body := []byte("payload data")
	r := newSignedRequest("PUT", "http://localhost:9000/obj", body, time.Now())
	if err := v.Verify(r); err != nil {
		t.Fatalf("verify: %v", err)
	}
	out, err := io.ReadAll(r.Body)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if !bytes.Equal(out, body) {
		t.Fatalf("body mismatch: %q", out)
	}
}

func TestHMACChains(t *testing.T) {
	k1 := hmacSHA256([]byte("k"), []byte("d"))
	k2 := hmacSHA256(k1, []byte("r"))
	if len(k2) != sha256.Size {
		t.Fatalf("unexpected key length %d", len(k2))
	}
}
