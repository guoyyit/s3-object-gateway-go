package storage

import (
	"context"
	"io"
	"strings"
	"testing"
)

func TestEmptyObject(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	if _, err := s.PutObject(ctx, "empty", strings.NewReader(""), ObjectMeta{}); err != nil {
		t.Fatal(err)
	}
	m, err := s.HeadObject(ctx, "empty")
	if err != nil {
		t.Fatal(err)
	}
	if m.Size != 0 {
		t.Fatalf("size %d", m.Size)
	}
	rd, err := s.GetObject(ctx, "empty", nil)
	if err != nil {
		t.Fatal(err)
	}
	b, err := io.ReadAll(rd.Body)
	rd.Body.Close()
	if err != nil || len(b) != 0 {
		t.Fatalf("empty get b=%q err=%v", b, err)
	}
}
