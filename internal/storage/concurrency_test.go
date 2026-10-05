package storage

import (
	"context"
	"fmt"
	"io"
	"strings"
	"sync"
	"testing"
)

// TestListConcurrentWrites pages repeatedly while writers add and delete
// objects. Every completed page must be internally consistent: keys in
// sorted order and no duplicate within the keys seen from a fixed start.
func TestListConcurrentWrites(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()

	for i := 0; i < 30; i++ {
		if _, err := s.PutObject(ctx, fmt.Sprintf("base/%04d", i), strings.NewReader("x"), ObjectMeta{}); err != nil {
			t.Fatal(err)
		}
	}

	stop := make(chan struct{})
	var wg sync.WaitGroup

	wg.Add(2)
	go func() {
		defer wg.Done()
		for i := 30; i < 120; i++ {
			select {
			case <-stop:
				return
			default:
			}
			_, _ = s.PutObject(ctx, fmt.Sprintf("base/%04d", i), strings.NewReader("y"), ObjectMeta{})
		}
	}()
	go func() {
		defer wg.Done()
		for i := 0; i < 60; i++ {
			select {
			case <-stop:
				return
			default:
			}
			_ = s.DeleteObject(ctx, fmt.Sprintf("base/%04d", i%40))
		}
	}()

	for i := 0; i < 20; i++ {
		marker := ""
		seen := map[string]bool{}
		pages := 0
		for {
			page, err := s.ListObjects(ctx, "base/", "", marker, 13)
			if err != nil {
				t.Fatal(err)
			}
			var prev string
			for _, c := range page.Contents {
				if prev != "" && c.Key <= prev {
					t.Fatalf("non-sorted page: %q after %q", c.Key, prev)
				}
				if seen[c.Key] {
					t.Fatalf("duplicate key across pages: %q", c.Key)
				}
				seen[c.Key] = true
				prev = c.Key
			}
			pages++
			if !page.IsTruncated {
				break
			}
			if page.NextMarker == marker {
				t.Fatal("marker not advancing")
			}
			marker = page.NextMarker
			if pages > 1000 {
				t.Fatal("paging did not terminate")
			}
		}
	}
	close(stop)
	wg.Wait()
}

// TestConcurrentMultipartComplete runs several complete/abort cycles
// concurrently to confirm completed objects are always whole.
func TestConcurrentMultipartComplete(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	var wg sync.WaitGroup
	for g := 0; g < 8; g++ {
		wg.Add(1)
		go func(g int) {
			defer wg.Done()
			id, err := s.InitiateMultipart(ctx, fmt.Sprintf("mp%d", g), ObjectMeta{})
			if err != nil {
				t.Error(err)
				return
			}
			body := strings.Repeat(string(rune('A'+g)), minPartSize)
			p, err := s.UploadPart(ctx, id, 1, strings.NewReader(body))
			if err != nil {
				t.Error(err)
				return
			}
			if _, err := s.CompleteMultipart(ctx, id, []PartInfo{{PartNumber: 1, ETag: p.ETag}}); err != nil {
				t.Error(err)
			}
		}(g)
	}
	wg.Wait()

	for g := 0; g < 8; g++ {
		key := fmt.Sprintf("mp%d", g)
		rd, err := s.GetObject(ctx, key, nil)
		if err != nil {
			t.Fatalf("completed object %s unreadable: %v", key, err)
		}
		data, _ := io.ReadAll(rd.Body)
		rd.Body.Close()
		if int64(len(data)) != minPartSize {
			t.Fatalf("%s size %d", key, len(data))
		}
		for i := range data {
			if data[i] != data[0] {
				t.Fatalf("%s mixed content", key)
			}
		}
	}
}
