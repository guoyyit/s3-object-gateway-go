package api

import (
	"encoding/xml"
	"net/http"
	"testing"
	"time"
)

func TestHTTPPutGetHeadDelete(t *testing.T) {
	c := newTestServer(t)

	hdr := http.Header{}
	hdr.Set("Content-Type", "text/plain")
	hdr.Set("X-Amz-Meta-Team", "core")
	hdr.Set("Cache-Control", "max-age=10")
	body := []byte("the quick brown fox")
	resp := c.req("PUT", "/obj.txt", body, hdr)
	if resp.StatusCode != 200 {
		t.Fatalf("put status %d", resp.StatusCode)
	}
	resp.Body.Close()

	resp = c.req("GET", "/obj.txt", nil, nil)
	if resp.StatusCode != 200 {
		t.Fatalf("get status %d", resp.StatusCode)
	}
	if got := readBody(t, resp); string(got) != string(body) {
		t.Fatalf("body = %q", got)
	}

	resp = c.req("HEAD", "/obj.txt", nil, nil)
	if resp.StatusCode != 200 {
		t.Fatalf("head status %d", resp.StatusCode)
	}
	if resp.Header.Get("Content-Type") != "text/plain" {
		t.Fatalf("content-type = %q", resp.Header.Get("Content-Type"))
	}
	if resp.Header.Get("X-Amz-Meta-Team") != "core" {
		t.Fatalf("metadata = %q", resp.Header.Get("X-Amz-Meta-Team"))
	}
	if resp.Header.Get("Cache-Control") != "max-age=10" {
		t.Fatalf("cache-control = %q", resp.Header.Get("Cache-Control"))
	}

	hdr = http.Header{}
	hdr.Set("Range", "bytes=4-8")
	resp = c.req("GET", "/obj.txt", nil, hdr)
	if resp.StatusCode != 206 {
		t.Fatalf("range status %d", resp.StatusCode)
	}
	if got := readBody(t, resp); string(got) != "quick" {
		t.Fatalf("range = %q", got)
	}

	hdr = http.Header{}
	hdr.Set("Range", "bytes=1000-2000")
	resp = c.req("GET", "/obj.txt", nil, hdr)
	if resp.StatusCode != 416 {
		t.Fatalf("bad range status %d", resp.StatusCode)
	}
	resp.Body.Close()

	resp = c.req("GET", "/nope", nil, nil)
	if resp.StatusCode != 404 {
		t.Fatalf("missing status %d", resp.StatusCode)
	}
	var xe struct {
		Code    string `xml:"Code"`
		Message string `xml:"Message"`
	}
	_ = xml.NewDecoder(resp.Body).Decode(&xe)
	resp.Body.Close()
	if xe.Code != "NoSuchKey" {
		t.Fatalf("error code = %q", xe.Code)
	}

	resp = c.req("DELETE", "/obj.txt", nil, nil)
	if resp.StatusCode != 204 {
		t.Fatalf("delete status %d", resp.StatusCode)
	}
	resp.Body.Close()
	resp = c.req("GET", "/obj.txt", nil, nil)
	if resp.StatusCode != 404 {
		t.Fatalf("after delete status %d", resp.StatusCode)
	}
	resp.Body.Close()
}

func TestHTTPSigV4Rejection(t *testing.T) {
	c := newTestServer(t)

	req, _ := http.NewRequest("GET", c.base+"/obj", nil)
	resp, err := c.client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != 403 {
		t.Fatalf("unsigned status %d", resp.StatusCode)
	}
	resp.Body.Close()

	c.secret = "wrongsecret"
	resp = c.req("GET", "/obj", nil, nil)
	if resp.StatusCode != 403 {
		t.Fatalf("bad sig status %d", resp.StatusCode)
	}
	resp.Body.Close()
	c.secret = secretKey

	c.now = time.Now().Add(-time.Hour).UTC()
	resp = c.req("GET", "/obj", nil, nil)
	if resp.StatusCode != 403 {
		t.Fatalf("skew status %d", resp.StatusCode)
	}
	var xe struct {
		Code string `xml:"Code"`
	}
	_ = xml.NewDecoder(resp.Body).Decode(&xe)
	resp.Body.Close()
	if xe.Code != "RequestTimeTooSkewed" {
		t.Fatalf("skew code = %q", xe.Code)
	}
}
