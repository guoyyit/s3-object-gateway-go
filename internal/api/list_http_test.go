package api

import (
	"encoding/xml"
	"net/url"
	"testing"
)

func TestHTTPListPaging(t *testing.T) {
	c := newTestServer(t)
	keys := []string{"a/1", "a/2", "b", "a/3"}
	for _, k := range keys {
		resp := c.req("PUT", "/"+k, []byte("x"), nil)
		resp.Body.Close()
	}

	var all []string
	marker := ""
	for {
		path := "/?list-type=2&max-keys=2"
		if marker != "" {
			path += "&continuation-token=" + url.QueryEscape(marker)
		}
		resp := c.req("GET", path, nil, nil)
		if resp.StatusCode != 200 {
			t.Fatalf("list %d", resp.StatusCode)
		}
		var res struct {
			Contents []struct {
				Key string `xml:"Key"`
			} `xml:"Contents"`
			IsTruncated bool   `xml:"IsTruncated"`
			NextMarker  string `xml:"NextMarker"`
		}
		if err := xml.NewDecoder(resp.Body).Decode(&res); err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		for _, ct := range res.Contents {
			all = append(all, ct.Key)
		}
		if !res.IsTruncated {
			break
		}
		marker = res.NextMarker
		if marker == "" {
			t.Fatal("truncated without next marker")
		}
	}

	want := map[string]bool{"a/1": true, "a/2": true, "a/3": true, "b": true}
	if len(all) != 4 {
		t.Fatalf("listed %v", all)
	}
	for _, k := range all {
		if !want[k] {
			t.Fatalf("unexpected key %q", k)
		}
	}
}

func TestHTTPListDelimiter(t *testing.T) {
	c := newTestServer(t)
	for _, k := range []string{"photos/a.jpg", "photos/b.jpg", "photos/2024/x.jpg", "notes.txt"} {
		resp := c.req("PUT", "/"+k, []byte("x"), nil)
		resp.Body.Close()
	}
	resp := c.req("GET", "/?list-type=2&prefix=photos/&delimiter=%2F", nil, nil)
	if resp.StatusCode != 200 {
		t.Fatalf("status %d", resp.StatusCode)
	}
	var res struct {
		CommonPrefixes []struct {
			Prefix string `xml:"Prefix"`
		} `xml:"CommonPrefixes"`
		Contents []struct {
			Key string `xml:"Key"`
		} `xml:"Contents"`
	}
	if err := xml.NewDecoder(resp.Body).Decode(&res); err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if len(res.CommonPrefixes) != 1 || res.CommonPrefixes[0].Prefix != "photos/2024/" {
		t.Fatalf("prefixes = %v", res.CommonPrefixes)
	}
	if len(res.Contents) != 2 {
		t.Fatalf("contents = %d", len(res.Contents))
	}
}

func TestHTTPListBuckets(t *testing.T) {
	c := newTestServer(t)
	resp := c.req("GET", "/", nil, nil)
	if resp.StatusCode != 200 {
		t.Fatalf("status %d", resp.StatusCode)
	}
	resp.Body.Close()
}
