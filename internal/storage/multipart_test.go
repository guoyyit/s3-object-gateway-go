package storage

import (
	"context"
	"io"
	"strings"
	"testing"
)

func TestMultipartLifecycle(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()

	id, err := s.InitiateMultipart(ctx, "big.bin", ObjectMeta{ContentType: "application/octet-stream"})
	if err != nil {
		t.Fatal(err)
	}

	// Incomplete upload must not appear as an object.
	if _, err := s.HeadObject(ctx, "big.bin"); err != ErrNotFound {
		t.Fatalf("incomplete upload readable: %v", err)
	}

	big := strings.Repeat("A", minPartSize)
	parts := make([]PartInfo, 0, 3)
	for i := 1; i <= 3; i++ {
		body := big
		if i == 3 {
			body = "tail"
		}
		p, err := s.UploadPart(ctx, id, i, strings.NewReader(body))
		if err != nil {
			t.Fatalf("part %d: %v", i, err)
		}
		parts = append(parts, *p)
	}

	// Parts listed back.
	listed, err := s.ListParts(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if len(listed) != 3 {
		t.Fatalf("listed parts = %d", len(listed))
	}

	// Wrong order must be rejected.
	bad := []PartInfo{parts[2], parts[0]}
	if _, err := s.CompleteMultipart(ctx, id, bad); err != ErrInvalidPartOrder {
		t.Fatalf("expected order error, got %v", err)
	}

	// Unknown part must be rejected.
	if _, err := s.CompleteMultipart(ctx, id, []PartInfo{{PartNumber: 99, ETag: `"dead"`}}); err != ErrInvalidPart {
		t.Fatalf("expected invalid part, got %v", err)
	}

	complete := []PartInfo{{PartNumber: parts[0].PartNumber, ETag: parts[0].ETag},
		{PartNumber: parts[1].PartNumber, ETag: parts[1].ETag},
		{PartNumber: parts[2].PartNumber, ETag: parts[2].ETag}}
	meta, err := s.CompleteMultipart(ctx, id, complete)
	if err != nil {
		t.Fatalf("complete: %v", err)
	}
	if meta.Size != int64(2*minPartSize+4) {
		t.Fatalf("size = %d", meta.Size)
	}
	if !strings.HasSuffix(strings.Trim(meta.ETag, `"`), "-3") {
		t.Fatalf("multipart etag = %q", meta.ETag)
	}

	rd, err := s.GetObject(ctx, "big.bin", nil)
	if err != nil {
		t.Fatal(err)
	}
	got := make([]byte, minPartSize*2+4)
	n, _ := io.ReadFull(rd.Body, got)
	rd.Body.Close()
	if n != len(got) {
		t.Fatalf("read %d bytes", n)
	}

	// Upload record is gone.
	if _, err := s.ListParts(ctx, id); err != ErrNotFound {
		t.Fatalf("upload not cleaned after complete: %v", err)
	}
}

func TestMultipartAbort(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	id, err := s.InitiateMultipart(ctx, "x", ObjectMeta{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.UploadPart(ctx, id, 1, strings.NewReader("data")); err != nil {
		t.Fatal(err)
	}
	if err := s.AbortMultipart(ctx, id); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ListParts(ctx, id); err != ErrNotFound {
		t.Fatalf("aborted upload still present: %v", err)
	}
}

func TestPartTooSmall(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	id, _ := s.InitiateMultipart(ctx, "x", ObjectMeta{})
	p, err := s.UploadPart(ctx, id, 1, strings.NewReader("tiny"))
	if err != nil {
		t.Fatal(err)
	}
	p2, err := s.UploadPart(ctx, id, 2, strings.NewReader("tiny2"))
	if err != nil {
		t.Fatal(err)
	}
	_, err = s.CompleteMultipart(ctx, id, []PartInfo{{PartNumber: 1, ETag: p.ETag},
		{PartNumber: 2, ETag: p2.ETag}})
	if err != ErrPartTooSmall {
		t.Fatalf("expected part too small, got %v", err)
	}
}
