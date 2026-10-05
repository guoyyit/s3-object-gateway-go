package auth

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"testing"
	"time"
)

// buildChunked constructs an aws-chunked body signed with the seed signature.
func buildChunked(t *testing.T, chunks [][]byte, key []byte, date, scope, seedSig string) []byte {
	t.Helper()
	prev := []byte(seedSig)
	var out bytes.Buffer
	for _, data := range chunks {
		sts := fmt.Sprintf("AWS4-HMAC-SHA256-PAYLOAD\n%s\n%s\n%s\n%s\n%s",
			date, scope, string(prev), emptySHA256, hexSHA256(data))
		sig := hmacSHA256(key, []byte(sts))
		fmt.Fprintf(&out, "%x;chunk-signature=%s\r\n", len(data), hex.EncodeToString(sig))
		out.Write(data)
		out.WriteString("\r\n")
		prev = sig
	}
	// Terminating zero chunk.
	sts := fmt.Sprintf("AWS4-HMAC-SHA256-PAYLOAD\n%s\n%s\n%s\n%s\n%s",
		date, scope, string(prev), emptySHA256, hexSHA256(nil))
	sig := hmacSHA256(key, []byte(sts))
	fmt.Fprintf(&out, "0;chunk-signature=%s\r\n\r\n", hex.EncodeToString(sig))
	return out.Bytes()
}

func TestChunkedVerifier(t *testing.T) {
	when := time.Now().UTC()
	date := when.Format("20060102")
	amzDate := when.Format(amzDateLayout)
	scope := date + "/" + testRegion + "/s3/aws4_request"
	key := deriveSigningKey(testSecretKey, date, testRegion)

	data := [][]byte{[]byte("chunk-one"), []byte("chunk-two")}

	// Compute the header-level signature with the streaming payload marker.
	canonical := "PUT\n/obj\n\nhost:localhost:9000\nx-amz-content-sha256:" + streamingPayload +
		"\nx-amz-date:" + amzDate + "\nhost;x-amz-content-sha256;x-amz-date\n" + streamingPayload
	sts := "AWS4-HMAC-SHA256\n" + amzDate + "\n" + scope + "\n" + hexSHA256([]byte(canonical))
	seedSig := hex.EncodeToString(hmacSHA256(key, []byte(sts)))

	body := buildChunked(t, data, key, date, scope, seedSig)

	r, _ := http.NewRequest("PUT", "http://localhost:9000/obj", bytes.NewReader(body))
	r.Host = "localhost:9000"
	r.Header.Set("X-Amz-Date", amzDate)
	r.Header.Set("X-Amz-Content-Sha256", streamingPayload)
	r.Header.Set("Authorization", "AWS4-HMAC-SHA256 Credential="+testAccessKey+"/"+scope+
		", SignedHeaders=host;x-amz-content-sha256;x-amz-date, Signature="+seedSig)

	v := NewVerifier(func(_ context.Context, k string) (string, bool, error) {
		return testSecretKey, k == testAccessKey, nil
	})
	if err := v.Verify(r); err != nil {
		t.Fatalf("verify: %v", err)
	}
	got, err := io.ReadAll(r.Body)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	want := append(append([]byte{}, data[0]...), data[1]...)
	if !bytes.Equal(got, want) {
		t.Fatalf("decoded = %q want %q", got, want)
	}
}

func TestChunkedVerifierTampered(t *testing.T) {
	when := time.Now().UTC()
	date := when.Format("20060102")
	amzDate := when.Format(amzDateLayout)
	scope := date + "/" + testRegion + "/s3/aws4_request"
	key := deriveSigningKey(testSecretKey, date, testRegion)

	canonical := "PUT\n/obj\n\nhost:localhost:9000\nx-amz-content-sha256:" + streamingPayload +
		"\nx-amz-date:" + amzDate + "\nhost;x-amz-content-sha256;x-amz-date\n" + streamingPayload
	sts := "AWS4-HMAC-SHA256\n" + amzDate + "\n" + scope + "\n" + hexSHA256([]byte(canonical))
	seedSig := hex.EncodeToString(hmacSHA256(key, []byte(sts)))

	body := buildChunked(t, [][]byte{[]byte("original")}, key, date, scope, seedSig)
	body = bytes.Replace(body, []byte("original"), []byte("originaX"), 1)

	r, _ := http.NewRequest("PUT", "http://localhost:9000/obj", bytes.NewReader(body))
	r.Host = "localhost:9000"
	r.Header.Set("X-Amz-Date", amzDate)
	r.Header.Set("X-Amz-Content-Sha256", streamingPayload)
	r.Header.Set("Authorization", "AWS4-HMAC-SHA256 Credential="+testAccessKey+"/"+scope+
		", SignedHeaders=host;x-amz-content-sha256;x-amz-date, Signature="+seedSig)

	v := NewVerifier(func(_ context.Context, k string) (string, bool, error) {
		return testSecretKey, k == testAccessKey, nil
	})
	if err := v.Verify(r); err != nil {
		t.Fatalf("verify: %v", err)
	}
	if _, err := io.Copy(io.Discard, r.Body); err != ErrBodySignature {
		t.Fatalf("expected ErrBodySignature, got %v", err)
	}
}

var _ = sha256.New
var _ = hmac.Equal
