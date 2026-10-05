package auth

import (
	"context"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"testing"
	"time"
)

func presignURL(t *testing.T, method, rawURL string, when time.Time) string {
	t.Helper()
	u, err := url.Parse(rawURL)
	if err != nil {
		t.Fatal(err)
	}
	when = when.UTC()
	date := when.Format("20060102")
	amzDate := when.Format(amzDateLayout)
	scope := date + "/" + testRegion + "/s3/aws4_request"

	q := u.Query()
	q.Set("X-Amz-Algorithm", algorithm)
	q.Set("X-Amz-Credential", testAccessKey+"/"+scope)
	q.Set("X-Amz-Date", amzDate)
	q.Set("X-Amz-Expires", "300")
	q.Set("X-Amz-SignedHeaders", "host")
	u.RawQuery = q.Encode()

	// Canonical query excludes X-Amz-Signature.
	keys := make([]string, 0, len(q))
	for k := range q {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var parts []string
	for _, k := range keys {
		parts = append(parts, awsEncode(k)+"="+awsEncode(q.Get(k)))
	}

	canonical := strings.Join([]string{
		method,
		u.Path,
		strings.Join(parts, "&"),
		"host:" + u.Host,
		"host",
		emptySHA256,
	}, "\n")
	sts := strings.Join([]string{
		algorithm,
		amzDate,
		scope,
		hexSHA256([]byte(canonical)),
	}, "\n")
	key := deriveSigningKey(testSecretKey, date, testRegion)
	sig := hexHMAC(key, []byte(sts))

	q.Set("X-Amz-Signature", sig)
	u.RawQuery = q.Encode()
	return u.String()
}

func TestPresignedValid(t *testing.T) {
	v := NewVerifier(func(_ context.Context, k string) (string, bool, error) {
		return testSecretKey, k == testAccessKey, nil
	})
	signed := presignURL(t, "GET", "http://localhost:9000/obj?download=1", time.Now())
	r, err := http.NewRequest("GET", signed, nil)
	if err != nil {
		t.Fatal(err)
	}
	r.Host = "localhost:9000"
	if err := v.Verify(r); err != nil {
		t.Fatalf("presigned verify: %v", err)
	}
}

func TestPresignedExpired(t *testing.T) {
	v := NewVerifier(func(_ context.Context, k string) (string, bool, error) {
		return testSecretKey, k == testAccessKey, nil
	})
	signed := presignURL(t, "GET", "http://localhost:9000/obj", time.Now().Add(-time.Hour))
	r, _ := http.NewRequest("GET", signed, nil)
	r.Host = "localhost:9000"
	if err := v.Verify(r); err != ErrTimeSkew {
		t.Fatalf("expected expiry/ErrTimeSkew, got %v", err)
	}
}

func TestPresignedTampered(t *testing.T) {
	v := NewVerifier(func(_ context.Context, k string) (string, bool, error) {
		return testSecretKey, k == testAccessKey, nil
	})
	signed := presignURL(t, "GET", "http://localhost:9000/obj", time.Now())
	r, _ := http.NewRequest("GET", signed+"&injected=x", nil)
	r.Host = "localhost:9000"
	if err := v.Verify(r); err != ErrSignature {
		t.Fatalf("expected ErrSignature, got %v", err)
	}
}
