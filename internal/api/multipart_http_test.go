package api

import (
	"encoding/xml"
	"fmt"
	"net/http"
	"strings"
	"testing"
)

func TestHTTPMultipartFlow(t *testing.T) {
	c := newTestServer(t)

	resp := c.req("POST", "/mp.bin?uploads", nil, nil)
	if resp.StatusCode != 200 {
		t.Fatalf("initiate %d: %s", resp.StatusCode, readBody(t, resp))
	}
	var init struct {
		UploadID string `xml:"UploadId"`
	}
	if err := xml.NewDecoder(resp.Body).Decode(&init); err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if init.UploadID == "" {
		t.Fatal("empty upload id")
	}

	partBodies := []string{strings.Repeat("A", 5*1024*1024), "final-part"}
	type part struct {
		n    int
		etag string
	}
	var parts []part
	for i, b := range partBodies {
		path := fmt.Sprintf("/mp.bin?partNumber=%d&uploadId=%s", i+1, init.UploadID)
		resp = c.req("PUT", path, []byte(b), nil)
		if resp.StatusCode != 200 {
			t.Fatalf("part %d %d", i+1, resp.StatusCode)
		}
		parts = append(parts, part{n: i + 1, etag: resp.Header.Get("ETag")})
		resp.Body.Close()
	}

	// Incomplete upload is not readable as an object.
	resp = c.req("GET", "/mp.bin", nil, nil)
	if resp.StatusCode != 404 {
		t.Fatalf("incomplete readable: %d", resp.StatusCode)
	}
	resp.Body.Close()

	var xmlParts strings.Builder
	xmlParts.WriteString("<CompleteMultipartUpload>")
	for _, p := range parts {
		fmt.Fprintf(&xmlParts, "<Part><PartNumber>%d</PartNumber><ETag>%s</ETag></Part>", p.n, p.etag)
	}
	xmlParts.WriteString("</CompleteMultipartUpload>")

	resp = c.req("POST", "/mp.bin?uploadId="+init.UploadID, []byte(xmlParts.String()),
		http.Header{"Content-Type": {"application/xml"}})
	if resp.StatusCode != 200 {
		t.Fatalf("complete %d: %s", resp.StatusCode, readBody(t, resp))
	}
	resp.Body.Close()

	resp = c.req("GET", "/mp.bin", nil, nil)
	if resp.StatusCode != 200 {
		t.Fatalf("get completed %d", resp.StatusCode)
	}
	got := readBody(t, resp)
	want := strings.Repeat("A", 5*1024*1024) + "final-part"
	if string(got) != want {
		t.Fatalf("completed content length %d want %d", len(got), len(want))
	}
}

func TestHTTPMultipartAbort(t *testing.T) {
	c := newTestServer(t)
	resp := c.req("POST", "/mp2?uploads", nil, nil)
	var init struct {
		UploadID string `xml:"UploadId"`
	}
	xml.NewDecoder(resp.Body).Decode(&init)
	resp.Body.Close()

	resp = c.req("PUT", "/mp2?partNumber=1&uploadId="+init.UploadID, []byte("data"), nil)
	resp.Body.Close()

	resp = c.req("DELETE", "/mp2?uploadId="+init.UploadID, nil, nil)
	if resp.StatusCode != 204 {
		t.Fatalf("abort %d", resp.StatusCode)
	}
	resp.Body.Close()

	// Completing an aborted upload fails.
	body := "<CompleteMultipartUpload><Part><PartNumber>1</PartNumber><ETag>\"x\"</ETag></Part></CompleteMultipartUpload>"
	resp = c.req("POST", "/mp2?uploadId="+init.UploadID, []byte(body), nil)
	if resp.StatusCode != 404 {
		t.Fatalf("complete aborted %d", resp.StatusCode)
	}
	resp.Body.Close()
}
