package server

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"
	"time"
)

const (
	testAccessKey = "AKIATEST"
	testSecretKey = "secret-test-key"
	testRegion    = "us-east-1"
	testService   = "s3"
)

// signRequest signs r with SigV4 header auth. body is the exact payload.
func signRequest(r *http.Request, body []byte, now time.Time) {
	sum := sha256.Sum256(body)
	payloadHash := hex.EncodeToString(sum[:])
	amzDate := now.UTC().Format("20060102T150405Z")
	date := now.UTC().Format("20060102")

	r.Header.Set("X-Amz-Date", amzDate)
	r.Header.Set("X-Amz-Content-Sha256", payloadHash)

	signedHeaders := []string{"host", "x-amz-content-sha256", "x-amz-date"}
	for k := range r.Header {
		name := strings.ToLower(k)
		if name == "host" || name == "x-amz-content-sha256" || name == "x-amz-date" {
			continue
		}
		signedHeaders = append(signedHeaders, name)
	}
	sort.Strings(signedHeaders)

	canonicalHeaders := "host:" + r.Host + "\n"
	for _, h := range signedHeaders {
		if h == "host" {
			continue
		}
		canonicalHeaders += h + ":" + strings.Join(strings.Fields(strings.TrimSpace(r.Header.Get(h))), " ") + "\n"
	}

	canonicalQuery := canonicalQueryForSigning(r)
	canonicalRequest := strings.Join([]string{
		r.Method,
		r.URL.EscapedPath(),
		canonicalQuery,
		canonicalHeaders,
		strings.Join(signedHeaders, ";"),
		payloadHash,
	}, "\n")

	crSum := sha256.Sum256([]byte(canonicalRequest))
	scope := fmt.Sprintf("%s/%s/%s/aws4_request", date, testRegion, testService)
	stringToSign := strings.Join([]string{
		"AWS4-HMAC-SHA256",
		amzDate,
		scope,
		hex.EncodeToString(crSum[:]),
	}, "\n")

	kDate := hmacSHA256Test([]byte("AWS4"+testSecretKey), date)
	kRegion := hmacSHA256Test(kDate, testRegion)
	kService := hmacSHA256Test(kRegion, testService)
	kSigning := hmacSHA256Test(kService, "aws4_request")
	sig := hex.EncodeToString(hmacSHA256Test(kSigning, stringToSign))

	r.Header.Set("Authorization", fmt.Sprintf(
		"AWS4-HMAC-SHA256 Credential=%s/%s, SignedHeaders=%s, Signature=%s",
		testAccessKey, scope, strings.Join(signedHeaders, ";"), sig))
}

func canonicalQueryForSigning(r *http.Request) string {
	values := r.URL.Query()
	keys := make([]string, 0, len(values))
	for k := range values {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := make([]string, 0)
	for _, k := range keys {
		for _, v := range values[k] {
			parts = append(parts, awsURIEncodeTest(k)+"="+awsURIEncodeTest(v))
		}
	}
	return strings.Join(parts, "&")
}

func hmacSHA256Test(key []byte, data string) []byte {
	m := hmac.New(sha256.New, key)
	m.Write([]byte(data))
	return m.Sum(nil)
}

func awsURIEncodeTest(s string) string {
	var b strings.Builder
	for _, c := range []byte(s) {
		switch {
		case (c >= 'A' && c <= 'Z') || (c >= 'a' && c <= 'z') || (c >= '0' && c <= '9'):
			b.WriteByte(c)
		case c == '-' || c == '_' || c == '.' || c == '~':
			b.WriteByte(c)
		default:
			b.WriteString(fmt.Sprintf("%%%02X", c))
		}
	}
	return b.String()
}

func newSignedRequest(method, target string, body []byte, now time.Time) *http.Request {
	var rdr io.Reader
	if body != nil {
		rdr = bytes.NewReader(body)
	}
	req, err := http.NewRequest(method, target, rdr)
	if err != nil {
		panic(err)
	}
	signRequest(req, body, now)
	return req
}
