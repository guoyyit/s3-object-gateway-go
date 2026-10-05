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
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/guoyyit/s3-object-gateway-go/internal/protocol"
)

// MaxSkew is the allowed request timestamp deviation (S3 default: 15 minutes).
const MaxSkew = 15 * time.Minute

// Identity records the authenticated principal.
type Identity struct {
	AccessKeyID string
}

type identityKey struct{}

// Verifier validates SigV4 authorization headers and presigned URLs.
type Verifier struct {
	store *Store
	now   func() time.Time
}

// NewVerifier builds a SigV4 verifier.
func NewVerifier(store *Store, now func() time.Time) *Verifier {
	if now == nil {
		now = time.Now
	}
	return &Verifier{store: store, now: now}
}

// Verify authenticates a request, returning an *protocol.S3Error on failure.
func (v *Verifier) Verify(r *http.Request) (*Identity, error) {
	if !v.store.Enabled() {
		return &Identity{}, nil
	}

	if r.URL.Query().Get("X-Amz-Signature") != "" {
		return v.verifyQuery(r)
	}
	return v.verifyHeader(r)
}

// WithIdentity stores the identity on a context.
func WithIdentity(ctx context.Context, id *Identity) context.Context {
	return context.WithValue(ctx, identityKey{}, id)
}

// IdentityFromContext retrieves the authenticated identity.
func IdentityFromContext(ctx context.Context) *Identity {
	if id, ok := ctx.Value(identityKey{}).(*Identity); ok {
		return id
	}
	return nil
}

const (
	amzDateFormat          = "20060102T150405Z"
	scopeDateLayout        = "20060102"
	unsignedPayload        = "UNSIGNED-PAYLOAD"
	streamingSignedPayload = "STREAMING-AWS4-HMAC-SHA256-PAYLOAD"
	emptySHA256            = "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855"
)

type parsedAuth struct {
	accessKeyID   string
	date          string
	region        string
	service       string
	signedHeaders []string
	signature     string
}

func (v *Verifier) verifyHeader(r *http.Request) (*Identity, error) {
	parsed, err := parseAuthorizationHeader(r.Header.Get("Authorization"))
	if err != nil {
		return nil, err
	}
	creds, ok := v.store.Lookup(parsed.accessKeyID)
	if !ok {
		return nil, protocol.ErrInvalidAccessKeyID
	}

	requestDate, err := requestTimestamp(r, parsed.date)
	if err != nil {
		return nil, err
	}
	if skew := v.now().Sub(requestDate); skew > MaxSkew || skew < -MaxSkew {
		return nil, protocol.ErrRequestTimeTooSkewed
	}

	declared := r.Header.Get("X-Amz-Content-Sha256")

	var payloadHash string
	var chunked bool
	switch {
	case declared == streamingSignedPayload:
		payloadHash = streamingSignedPayload
		chunked = true
	case declared == "":
		payloadHash, err = spoolAndHash(r)
		if err != nil {
			return nil, protocol.AsS3Error(err)
		}
	case declared == unsignedPayload:
		payloadHash = unsignedPayload
	case len(declared) == 64:
		payloadHash, err = spoolAndHash(r)
		if err != nil {
			return nil, protocol.AsS3Error(err)
		}
		if payloadHash != declared {
			return nil, protocol.ErrContentSHA256Mismatch
		}
	default:
		return nil, protocol.ErrContentSHA256Mismatch
	}

	cr := canonicalRequest(r, parsed.signedHeaders, payloadHash)
	sts := stringToSign(requestDate, parsed.date, parsed.region, parsed.service, cr)
	signingKey := deriveSigningKey(creds.SecretAccessKey, parsed.date, parsed.region, parsed.service)
	expected := signature(signingKey, sts)
	if !hmac.Equal([]byte(expected), []byte(parsed.signature)) {
		return nil, protocol.ErrSignatureDoesNotMatch
	}

	if chunked {
		decoded, derr := newChunkedReader(r.Body, signingKey, sts, parsed.signature)
		if derr != nil {
			return nil, derr
		}
		r.Body = decoded
	}
	return &Identity{AccessKeyID: creds.AccessKeyID}, nil
}

func (v *Verifier) verifyQuery(r *http.Request) (*Identity, error) {
	q := r.URL.Query()
	cred := q.Get("X-Amz-Credential")
	parts := strings.Split(cred, "/")
	if len(parts) != 5 || parts[4] != "aws4_request" {
		return nil, protocol.ErrAuthorizationHeaderMalformed
	}
	accessKeyID := parts[0]
	creds, ok := v.store.Lookup(accessKeyID)
	if !ok {
		return nil, protocol.ErrInvalidAccessKeyID
	}
	signedHeaders := strings.Split(q.Get("X-Amz-SignedHeaders"), ";")
	signatureGiven := q.Get("X-Amz-Signature")

	requestDate, err := time.Parse(amzDateFormat, q.Get("X-Amz-Date"))
	if err != nil {
		return nil, protocol.ErrAuthorizationHeaderMalformed
	}
	if exp, convErr := strconv.Atoi(q.Get("X-Amz-Expires")); convErr == nil && v.now().After(requestDate.Add(time.Duration(exp)*time.Second)) {
		return nil, protocol.ErrExpiredToken
	}
	if skew := v.now().Sub(requestDate); skew > MaxSkew || skew < -MaxSkew {
		return nil, protocol.ErrRequestTimeTooSkewed
	}

	cr := canonicalRequest(r, signedHeaders, unsignedPayload)
	signingKey := deriveSigningKey(creds.SecretAccessKey, parts[1], parts[2], parts[3])
	sts := stringToSign(requestDate, parts[1], parts[2], parts[3], cr)
	expected := signature(signingKey, sts)
	if !hmac.Equal([]byte(expected), []byte(signatureGiven)) {
		return nil, protocol.ErrSignatureDoesNotMatch
	}
	return &Identity{AccessKeyID: accessKeyID}, nil
}

func parseAuthorizationHeader(header string) (*parsedAuth, error) {
	if !strings.HasPrefix(header, "AWS4-HMAC-SHA256 ") {
		return nil, protocol.ErrAuthorizationHeaderMalformed
	}
	fields := strings.Split(strings.TrimPrefix(header, "AWS4-HMAC-SHA256 "), ",")
	out := &parsedAuth{}
	for _, f := range fields {
		f = strings.TrimSpace(f)
		kv := strings.SplitN(f, "=", 2)
		if len(kv) != 2 {
			return nil, protocol.ErrAuthorizationHeaderMalformed
		}
		switch kv[0] {
		case "Credential":
			credParts := strings.Split(kv[1], "/")
			if len(credParts) != 5 || credParts[4] != "aws4_request" {
				return nil, protocol.ErrAuthorizationHeaderMalformed
			}
			out.accessKeyID = credParts[0]
			out.date = credParts[1]
			out.region = credParts[2]
			out.service = credParts[3]
		case "SignedHeaders":
			out.signedHeaders = strings.Split(kv[1], ";")
		case "Signature":
			out.signature = kv[1]
		}
	}
	if out.accessKeyID == "" || len(out.signedHeaders) == 0 || len(out.signature) != 64 {
		return nil, protocol.ErrAuthorizationHeaderMalformed
	}
	return out, nil
}

func requestTimestamp(r *http.Request, scopeDate string) (time.Time, error) {
	if amzDate := r.Header.Get("X-Amz-Date"); amzDate != "" {
		t, err := time.Parse(amzDateFormat, amzDate)
		if err != nil {
			return time.Time{}, protocol.ErrAuthorizationHeaderMalformed
		}
		if scopeDate != t.UTC().Format(scopeDateLayout) {
			return time.Time{}, protocol.ErrAuthorizationHeaderMalformed
		}
		return t, nil
	}
	if r.Header.Get("Date") == "" {
		return time.Time{}, protocol.ErrAuthorizationHeaderMalformed
	}
	t, err := http.ParseTime(r.Header.Get("Date"))
	if err != nil {
		return time.Time{}, protocol.ErrRequestTimeTooSkewed
	}
	return t, nil
}

// spoolAndHash reads the whole request body, replaces it with a fresh
// reader, and returns the hex SHA-256 of the payload.
func spoolAndHash(r *http.Request) (string, error) {
	if r.Body == nil {
		return emptySHA256, nil
	}
	data, err := io.ReadAll(r.Body)
	_ = r.Body.Close()
	if err != nil {
		return "", err
	}
	r.Body = io.NopCloser(bytes.NewReader(data))
	r.ContentLength = int64(len(data))
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:]), nil
}

func canonicalRequest(r *http.Request, signedHeaders []string, payloadHash string) string {
	var b strings.Builder
	b.WriteString(r.Method)
	b.WriteString("\n")
	b.WriteString(canonicalURI(r))
	b.WriteString("\n")
	b.WriteString(canonicalQuery(r))
	b.WriteString("\n")

	headers := make([]string, 0, len(signedHeaders))
	values := make(map[string]string, len(signedHeaders))
	for _, h := range signedHeaders {
		name := strings.ToLower(h)
		headers = append(headers, name)
		if name == "host" {
			values[name] = canonicalHeaderValue(r.Host)
			continue
		}
		values[name] = canonicalHeaderValue(r.Header.Get(h))
	}
	sort.Strings(headers)
	for _, h := range headers {
		b.WriteString(h)
		b.WriteString(":")
		b.WriteString(values[h])
		b.WriteString("\n")
	}
	b.WriteString("\n")
	b.WriteString(strings.Join(headers, ";"))
	b.WriteString("\n")
	b.WriteString(payloadHash)
	return b.String()
}

func canonicalHeaderValue(v string) string {
	return strings.Join(strings.Fields(strings.TrimSpace(v)), " ")
}

func canonicalURI(r *http.Request) string {
	path := r.URL.EscapedPath()
	if path == "" {
		return "/"
	}
	return path
}

func canonicalQuery(r *http.Request) string {
	values := r.URL.Query()
	keys := make([]string, 0, len(values))
	for k := range values {
		if k == "X-Amz-Signature" {
			continue
		}
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := make([]string, 0)
	for _, k := range keys {
		for _, vv := range values[k] {
			parts = append(parts, awsURIEncode(k)+"="+awsURIEncode(vv))
		}
	}
	return strings.Join(parts, "&")
}

func awsURIEncode(s string) string {
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

func stringToSign(t time.Time, date, region, service, canonicalRequest string) string {
	return strings.Join([]string{
		"AWS4-HMAC-SHA256",
		t.UTC().Format(amzDateFormat),
		date + "/" + region + "/" + service + "/aws4_request",
		hex.EncodeToString(sha256Sum([]byte(canonicalRequest))),
	}, "\n")
}

func sha256Sum(data []byte) []byte {
	h := sha256.Sum256(data)
	return h[:]
}

func hmacSHA256(key []byte, data string) []byte {
	m := hmac.New(sha256.New, key)
	_, _ = m.Write([]byte(data))
	return m.Sum(nil)
}

func deriveSigningKey(secret, date, region, service string) []byte {
	kDate := hmacSHA256([]byte("AWS4"+secret), date)
	kRegion := hmacSHA256(kDate, region)
	kService := hmacSHA256(kRegion, service)
	return hmacSHA256(kService, "aws4_request")
}

func signature(key []byte, sts string) string {
	return hex.EncodeToString(hmacSHA256(key, sts))
}
