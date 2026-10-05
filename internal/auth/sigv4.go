// Package auth implements AWS Signature Version 4 request verification.
package auth

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/md5"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"hash"
	"io"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"
)

// ErrSignature is returned when a request fails SigV4 verification.
var ErrSignature = errors.New("invalid signature")

// ErrBodySignature is returned when a request body fails payload integrity
// verification (chunk signature, payload hash, or Content-MD5). It surfaces
// while the wrapped request body is being read.
var ErrBodySignature = errors.New("payload signature mismatch")

// ErrInvalidKey indicates an unknown access key id.
var ErrInvalidKey = errors.New("invalid access key id")

// ErrTimeSkew indicates the request timestamp is outside the allowed window.
var ErrTimeSkew = errors.New("request time too skewed")

const (
	algorithm        = "AWS4-HMAC-SHA256"
	terminator       = "aws4_request"
	serviceName      = "s3"
	amzDateLayout    = "20060102T150405Z"
	maxHeaderSkew    = 15 * time.Minute
	unsignedPayload  = "UNSIGNED-PAYLOAD"
	streamingPayload = "STREAMING-AWS4-HMAC-SHA256-PAYLOAD"
	emptySHA256      = "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855"
)

// AccessKeyLookup resolves an access key id to its secret access key.
type AccessKeyLookup func(ctx context.Context, accessKey string) (string, bool, error)

// Verifier checks SigV4 signatures on incoming requests.
type Verifier struct {
	lookup AccessKeyLookup
	now    func() time.Time
}

// NewVerifier constructs a Verifier using the given key lookup.
func NewVerifier(lookup AccessKeyLookup) *Verifier {
	return &Verifier{lookup: lookup, now: time.Now}
}

// Verify validates the Authorization header or query parameters of a request.
// On success the request body may be replaced with a reader that verifies
// payload integrity as it is consumed; failures are reported through Read.
func (v *Verifier) Verify(r *http.Request) error {
	creds, err := parseAuth(r)
	if err != nil {
		return err
	}

	secret, ok, err := v.lookup(r.Context(), creds.accessKey)
	if err != nil {
		return err
	}
	if !ok {
		return ErrInvalidKey
	}

	if err := creds.checkTime(v.now()); err != nil {
		if errors.Is(err, ErrSignature) {
			return ErrTimeSkew
		}
		return err
	}

	signingKey := deriveSigningKey(secret, creds.date, creds.region)

	payloadHash := creds.payloadHash
	streaming := payloadHash == streamingPayload
	var buffered []byte
	if payloadHash == "" {
		if requestHasBody(r) {
			buffered, err = io.ReadAll(r.Body)
			if err != nil {
				return err
			}
			sum := sha256.Sum256(buffered)
			payloadHash = hex.EncodeToString(sum[:])
		} else {
			payloadHash = emptySHA256
		}
	}

	canonical := buildCanonicalRequest(r, creds.signedHeaders, payloadHash, creds.queryAuth)
	scope := creds.scope()
	stringToSign := strings.Join([]string{
		algorithm,
		creds.amzDate,
		scope,
		hexSHA256([]byte(canonical)),
	}, "\n")

	expected := hexHMAC(signingKey, []byte(stringToSign))
	if !hmac.Equal([]byte(expected), []byte(creds.signature)) {
		return ErrSignature
	}

	var base io.ReadCloser
	if buffered != nil {
		base = io.NopCloser(bytes.NewReader(buffered))
	} else {
		base = r.Body
	}

	wantMD5 := r.Header.Get("Content-Md5")
	if streaming {
		r.Body = newChunkedVerifier(base, signingKey, creds.date, scope, creds.signature, wantMD5)
	} else if payloadHash != unsignedPayload {
		r.Body = newHashVerifier(base, payloadHash, wantMD5)
	}
	return nil
}

type credentials struct {
	accessKey     string
	date          string
	region        string
	amzDate       string
	signedHeaders []string
	signature     string
	payloadHash   string
	queryAuth     bool
	expires       time.Duration
}

func (c *credentials) scope() string {
	return strings.Join([]string{c.date, c.region, serviceName, terminator}, "/")
}

func parseAuth(r *http.Request) (*credentials, error) {
	if r.URL.Query().Get("X-Amz-Algorithm") == algorithm {
		return parseQueryAuth(r)
	}
	return parseHeaderAuth(r)
}

func parseHeaderAuth(r *http.Request) (*credentials, error) {
	authz := r.Header.Get("Authorization")
	if authz == "" || !strings.HasPrefix(authz, algorithm+" ") {
		return nil, ErrSignature
	}
	params := parseAuthParams(strings.TrimPrefix(authz, algorithm+" "))
	c := &credentials{
		amzDate:     r.Header.Get("X-Amz-Date"),
		payloadHash: r.Header.Get("X-Amz-Content-Sha256"),
		signature:   params["Signature"],
	}
	credential := params["Credential"]
	signedHeaders := params["SignedHeaders"]
	if credential == "" || signedHeaders == "" || c.signature == "" || c.amzDate == "" {
		return nil, ErrSignature
	}
	parts := strings.SplitN(credential, "/", 5)
	if len(parts) != 5 || parts[3] != serviceName || parts[4] != terminator {
		return nil, ErrSignature
	}
	c.accessKey, c.date, c.region = parts[0], parts[1], parts[2]
	c.signedHeaders = strings.Split(signedHeaders, ";")
	return c, nil
}

func parseQueryAuth(r *http.Request) (*credentials, error) {
	q := r.URL.Query()
	c := &credentials{
		queryAuth: true,
		amzDate:   q.Get("X-Amz-Date"),
		signature: q.Get("X-Amz-Signature"),
	}
	credential := q.Get("X-Amz-Credential")
	signedHeaders := q.Get("X-Amz-SignedHeaders")
	if credential == "" || signedHeaders == "" || c.signature == "" || c.amzDate == "" {
		return nil, ErrSignature
	}
	parts := strings.SplitN(credential, "/", 5)
	if len(parts) != 5 || parts[3] != serviceName || parts[4] != terminator {
		return nil, ErrSignature
	}
	c.accessKey, c.date, c.region = parts[0], parts[1], parts[2]
	c.signedHeaders = strings.Split(signedHeaders, ";")
	if exp := q.Get("X-Amz-Expires"); exp != "" {
		secs, err := strconv.Atoi(exp)
		if err != nil || secs < 1 {
			return nil, ErrSignature
		}
		c.expires = time.Duration(secs) * time.Second
	}
	if h := q.Get("X-Amz-Content-Sha256"); h != "" {
		c.payloadHash = h
	}
	return c, nil
}

func parseAuthParams(s string) map[string]string {
	out := map[string]string{}
	for _, part := range strings.Split(s, ",") {
		part = strings.TrimSpace(part)
		k, v, ok := strings.Cut(part, "=")
		if ok {
			out[k] = v
		}
	}
	return out
}

func (c *credentials) checkTime(now time.Time) error {
	now = now.UTC()
	t, err := time.Parse(amzDateLayout, c.amzDate)
	if err != nil || len(c.date) != 8 || c.amzDate[:8] != c.date {
		return ErrSignature
	}
	t = t.UTC()
	if c.queryAuth {
		expiry := c.expires
		if expiry == 0 {
			expiry = 5 * time.Minute
		}
		// Allow the standard clock-skew tolerance at the lower bound.
		if now.Before(t.Add(-maxHeaderSkew)) || now.After(t.Add(expiry+maxHeaderSkew)) {
			return ErrTimeSkew
		}
		return nil
	}
	diff := now.Sub(t)
	if diff > maxHeaderSkew || diff < -maxHeaderSkew {
		return ErrTimeSkew
	}
	return nil
}

func buildCanonicalRequest(r *http.Request, signedHeaders []string, payloadHash string, queryAuth bool) string {
	return strings.Join([]string{
		r.Method,
		canonicalURI(r),
		canonicalQuery(r, queryAuth),
		strings.TrimSuffix(canonicalHeaders(r, signedHeaders), "\n"),
		strings.Join(normalizeHeaders(signedHeaders), ";"),
		payloadHash,
	}, "\n")
}

func canonicalURI(r *http.Request) string {
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

func canonicalQuery(r *http.Request, queryAuth bool) string {
	q := r.URL.Query()
	keys := make([]string, 0, len(q))
	for k := range q {
		if queryAuth && k == "X-Amz-Signature" {
			continue
		}
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		values := append([]string(nil), q[k]...)
		sort.Strings(values)
		ek := awsEncode(k)
		for _, val := range values {
			parts = append(parts, ek+"="+awsEncode(val))
		}
	}
	return strings.Join(parts, "&")
}

func canonicalHeaders(r *http.Request, signedHeaders []string) string {
	var b strings.Builder
	for _, h := range normalizeHeaders(signedHeaders) {
		var value string
		if h == "host" {
			value = r.Host
		} else {
			value = strings.Join(r.Header[http.CanonicalHeaderKey(h)], ",")
		}
		value = strings.Join(strings.Fields(value), " ")
		b.WriteString(h)
		b.WriteByte(':')
		b.WriteString(value)
		b.WriteByte('\n')
	}
	return b.String()
}

func normalizeHeaders(in []string) []string {
	out := append([]string(nil), in...)
	for i := range out {
		out[i] = strings.ToLower(strings.TrimSpace(out[i]))
	}
	sort.Strings(out)
	return out
}

// awsEncode percent-encodes every byte except unreserved characters.
func awsEncode(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		c := s[i]
		if (c >= 'A' && c <= 'Z') || (c >= 'a' && c <= 'z') ||
			(c >= '0' && c <= '9') || c == '-' || c == '_' || c == '.' || c == '~' {
			b.WriteByte(c)
			continue
		}
		fmt.Fprintf(&b, "%%%02X", c)
	}
	return b.String()
}

func deriveSigningKey(secret, date, region string) []byte {
	kDate := hmacSHA256([]byte("AWS4"+secret), []byte(date))
	kRegion := hmacSHA256(kDate, []byte(region))
	kService := hmacSHA256(kRegion, []byte(serviceName))
	return hmacSHA256(kService, []byte(terminator))
}

func hmacSHA256(key, data []byte) []byte {
	m := hmac.New(sha256.New, key)
	m.Write(data)
	return m.Sum(nil)
}

func hexHMAC(key, data []byte) string {
	return hex.EncodeToString(hmacSHA256(key, data))
}

func hexSHA256(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func requestHasBody(r *http.Request) bool {
	return r.Body != nil && r.ContentLength != 0
}

// hashVerifier checks the full-body SHA-256 payload hash and optional
// Content-MD5 once the body has been consumed.
type hashVerifier struct {
	body    io.ReadCloser
	h       hash.Hash
	md5h    hash.Hash
	want    string
	wantMD5 string
	err     error
}

func newHashVerifier(body io.ReadCloser, wantSHA, wantMD5 string) *hashVerifier {
	v := &hashVerifier{body: body, h: sha256.New(), want: strings.ToLower(wantSHA), wantMD5: wantMD5}
	if wantMD5 != "" {
		v.md5h = md5.New()
	}
	return v
}

func (v *hashVerifier) Read(p []byte) (int, error) {
	if v.err != nil {
		return 0, v.err
	}
	n, err := v.body.Read(p)
	if n > 0 {
		v.h.Write(p[:n])
		if v.md5h != nil {
			v.md5h.Write(p[:n])
		}
	}
	if errors.Is(err, io.EOF) {
		if cerr := v.check(); cerr != nil {
			v.err = cerr
			return n, cerr
		}
	}
	return n, err
}

func (v *hashVerifier) Close() error {
	return v.body.Close()
}

func (v *hashVerifier) check() error {
	if hex.EncodeToString(v.h.Sum(nil)) != v.want {
		return ErrBodySignature
	}
	if v.md5h != nil {
		want, err := base64.StdEncoding.DecodeString(v.wantMD5)
		if err != nil || !bytes.Equal(want, v.md5h.Sum(nil)) {
			return ErrBodySignature
		}
	}
	return nil
}
