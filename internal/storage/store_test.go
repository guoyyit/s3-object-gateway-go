package storage

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func openTestStore(t *testing.T) *Store {
	t.Helper()
	dir := t.TempDir()
	s, err := Open(dir)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func TestPutGetDelete(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()

	if _, err := s.HeadObject(ctx, "missing"); err != ErrNotFound {
		t.Fatalf("expected not found, got %v", err)
	}

	body := strings.NewReader("hello object store")
	meta, err := s.PutObject(ctx, "a/b.txt", body, ObjectMeta{
		ContentType: "text/plain",
		Metadata:    map[string]string{"Author": "tester"},
	})
	if err != nil {
		t.Fatalf("put: %v", err)
	}
	if meta.Size != 18 {
		t.Fatalf("size = %d", meta.Size)
	}

	rd, err := s.GetObject(ctx, "a/b.txt", nil)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	data, _ := io.ReadAll(rd.Body)
	rd.Body.Close()
	if string(data) != "hello object store" {
		t.Fatalf("data = %q", data)
	}
	if rd.Meta.Metadata["Author"] != "tester" {
		t.Fatalf("metadata lost: %+v", rd.Meta.Metadata)
	}

	if err := s.DeleteObject(ctx, "a/b.txt"); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if _, err := s.HeadObject(ctx, "a/b.txt"); err != ErrNotFound {
		t.Fatalf("expected deleted, got %v", err)
	}
}

func TestRanges(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	if _, err := s.PutObject(ctx, "k", strings.NewReader("0123456789"), ObjectMeta{}); err != nil {
		t.Fatal(err)
	}

	cases := []struct {
		header string
		want   string
		err    bool
	}{
		{"bytes=0-3", "0123", false},
		{"bytes=5-", "56789", false},
		{"bytes=-4", "6789", false},
		{"bytes=0-100", "0123456789", false},
		{"bytes=10-", "", true},
		{"bytes=5-3", "", true},
		{"bytes=-0", "", true},
	}
	for _, c := range cases {
		rng, err := ParseRange(c.header, 10)
		if c.err {
			if err == nil {
				t.Fatalf("%s expected error", c.header)
			}
			continue
		}
		if err != nil {
			t.Fatalf("%s parse: %v", c.header, err)
		}
		rd, err := s.GetObject(ctx, "k", rng)
		if err != nil {
			t.Fatalf("%s get: %v", c.header, err)
		}
		data, _ := io.ReadAll(rd.Body)
		rd.Body.Close()
		if string(data) != c.want {
			t.Fatalf("%s = %q want %q", c.header, data, c.want)
		}
	}
}

func TestRangeOnEmptyObject(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	if _, err := s.PutObject(ctx, "empty", strings.NewReader(""), ObjectMeta{}); err != nil {
		t.Fatal(err)
	}
	rng, err := ParseRange("bytes=0-", 0)
	if err == nil {
		_ = rng
		t.Fatal("expected invalid range on empty object")
	}
}

func TestListPagingAndDelimiter(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	keys := []string{
		"photos/a.jpg", "photos/b.jpg", "photos/2024/x.jpg",
		"notes.txt", "photos/c.jpg",
	}
	for _, k := range keys {
		if _, err := s.PutObject(ctx, k, strings.NewReader("x"), ObjectMeta{}); err != nil {
			t.Fatal(err)
		}
	}

	// Delimiter grouping.
	page, err := s.ListObjects(ctx, "photos/", "/", "", 100)
	if err != nil {
		t.Fatal(err)
	}
	if len(page.CommonPrefixes) != 1 || page.CommonPrefixes[0] != "photos/2024/" {
		t.Fatalf("common prefixes = %v", page.CommonPrefixes)
	}
	if len(page.Contents) != 3 {
		t.Fatalf("contents = %d", len(page.Contents))
	}

	// Pagination: page through with maxKeys=2 and ensure no dupes/gaps.
	var got []string
	marker := ""
	for {
		p, err := s.ListObjects(ctx, "", "", marker, 2)
		if err != nil {
			t.Fatal(err)
		}
		for _, c := range p.Contents {
			got = append(got, c.Key)
		}
		if !p.IsTruncated {
			break
		}
		if p.NextMarker == "" || p.NextMarker == marker {
			t.Fatalf("marker did not advance: %q", p.NextMarker)
		}
		marker = p.NextMarker
	}
	if len(got) != len(keys) {
		t.Fatalf("paged %d keys, want %d (%v)", len(got), len(keys), got)
	}
	seen := map[string]int{}
	for _, k := range got {
		seen[k]++
	}
	for _, k := range keys {
		if seen[k] != 1 {
			t.Fatalf("key %q seen %d times", k, seen[k])
		}
	}
}

func TestConcurrentPutConverges(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	const n = 20
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			body := strings.Repeat(string(rune('A'+i%26)), 100+i)
			_, _ = s.PutObject(ctx, "race", strings.NewReader(body), ObjectMeta{})
		}(i)
	}
	wg.Wait()

	rd, err := s.GetObject(ctx, "race", nil)
	if err != nil {
		t.Fatal(err)
	}
	data, _ := io.ReadAll(rd.Body)
	rd.Body.Close()
	if int64(len(data)) != rd.Meta.Size {
		t.Fatalf("size mismatch: header %d body %d", rd.Meta.Size, len(data))
	}
	for i := 1; i < len(data); i++ {
		if data[i] != data[0] {
			t.Fatal("observed mixed/partial version content")
		}
	}
}

func TestRestartPersistenceAndRecovery(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if _, err := s.PutObject(ctx, "keep", strings.NewReader("durable"), ObjectMeta{}); err != nil {
		t.Fatal(err)
	}
	// Start a multipart upload and write one part, then crash (close).
	id, err := s.InitiateMultipart(ctx, "mp", ObjectMeta{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.UploadPart(ctx, id, 1, strings.NewReader(strings.Repeat("z", 10))); err != nil {
		t.Fatal(err)
	}
	// Leave a junk tmp file to prove recovery cleans it.
	if err := os.WriteFile(filepath.Join(dir, "tmp", "garbage"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	s.Close()

	// Reopen: committed object survives, incomplete upload is recognized and
	// still continuable/abortable, tmp scratch is removed.
	s2, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer s2.Close()

	rd, err := s2.GetObject(ctx, "keep", nil)
	if err != nil {
		t.Fatalf("object lost after restart: %v", err)
	}
	data, _ := io.ReadAll(rd.Body)
	rd.Body.Close()
	if string(data) != "durable" {
		t.Fatalf("content = %q", data)
	}

	if _, err := s2.ListParts(ctx, id); err != nil {
		t.Fatalf("incomplete upload should be recognized after restart: %v", err)
	}
	if err := s2.AbortMultipart(ctx, id); err != nil {
		t.Fatalf("abort recovered upload: %v", err)
	}

	if entries, _ := os.ReadDir(filepath.Join(dir, "tmp")); len(entries) != 0 {
		t.Fatalf("tmp not cleaned: %v", entries)
	}
}
