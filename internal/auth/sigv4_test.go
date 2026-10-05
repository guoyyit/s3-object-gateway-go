package auth

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

var presignBase = time.Unix(1_700_000_000, 0).UTC()

func presignedRequest(signedAt time.Time, expires int) *http.Request {
	r := httptest.NewRequest(http.MethodGet, "http://localhost/bucket/key", nil)
	r.URL.RawQuery = signQuery(r, "AK", "SECRET", signedAt, expires)
	return r
}

func TestPresignedURLValid(t *testing.T) {
	v := NewVerifier(NewStore([]Credentials{{AccessKeyID: "AK", SecretAccessKey: "SECRET"}}), func() time.Time {
		return presignBase
	})
	r := presignedRequest(presignBase, 3600)
	id, err := v.Verify(r)
	if err != nil {
		t.Fatalf("valid presigned url rejected: %v", err)
	}
	if id.AccessKeyID != "AK" {
		t.Fatalf("identity %q", id.AccessKeyID)
	}
}

func TestPresignedURLExpired(t *testing.T) {
	v := NewVerifier(NewStore([]Credentials{{AccessKeyID: "AK", SecretAccessKey: "SECRET"}}), func() time.Time {
		return presignBase.Add(2 * time.Hour)
	})
	r := presignedRequest(presignBase, 60)
	if _, err := v.Verify(r); err == nil {
		t.Fatal("expired presigned url accepted")
	}
}

func TestPresignedURLTampered(t *testing.T) {
	v := NewVerifier(NewStore([]Credentials{{AccessKeyID: "AK", SecretAccessKey: "SECRET"}}), func() time.Time {
		return presignBase
	})
	r := presignedRequest(presignBase, 3600)
	r.URL.Path = "/bucket/other"
	if _, err := v.Verify(r); err == nil {
		t.Fatal("tampered presigned url accepted")
	}
}
