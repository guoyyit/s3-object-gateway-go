package auth

import (
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"
)

// signQuery builds an S3-style presigned query string for r.
func signQuery(r *http.Request, accessKey, secret string, now time.Time, expires int) string {
	date := now.UTC().Format("20060102")
	amzDate := now.UTC().Format("20060102T150405Z")
	scope := date + "/us-east-1/s3/aws4_request"

	params := map[string]string{
		"X-Amz-Algorithm":     "AWS4-HMAC-SHA256",
		"X-Amz-Credential":    accessKey + "/" + scope,
		"X-Amz-Date":          amzDate,
		"X-Amz-Expires":       strconv.Itoa(expires),
		"X-Amz-SignedHeaders": "host",
	}

	keys := make([]string, 0, len(params))
	for k := range params {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	var canonicalQuery strings.Builder
	for _, k := range keys {
		if canonicalQuery.Len() > 0 {
			canonicalQuery.WriteString("&")
		}
		canonicalQuery.WriteString(awsURIEncode(k) + "=" + awsURIEncode(params[k]))
	}

	canonicalRequest := strings.Join([]string{
		http.MethodGet,
		r.URL.EscapedPath(),
		canonicalQuery.String(),
		"host:" + r.Host + "\n",
		"host",
		"UNSIGNED-PAYLOAD",
	}, "\n")

	crSum := sha256.Sum256([]byte(canonicalRequest))
	sts := strings.Join([]string{
		"AWS4-HMAC-SHA256",
		amzDate,
		scope,
		hex.EncodeToString(crSum[:]),
	}, "\n")

	key := deriveSigningKey(secret, date, "us-east-1", "s3")
	sig := hex.EncodeToString(hmacSHA256(key, sts))

	return canonicalQuery.String() + "&X-Amz-Signature=" + url.QueryEscape(sig)
}
