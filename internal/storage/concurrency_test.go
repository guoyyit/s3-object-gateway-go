package storage

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"sync"
	"testing"
)

func TestConcurrentWritesConverge(t *testing.T) {
	store, err := Open(context.Background(), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ctx := context.Background()
	if err := store.CreateBucket(ctx, "b"); err != nil {
		t.Fatal(err)
	}

	const writers = 16
	var wg sync.WaitGroup
	errCh := make(chan error, writers)
	for i := 0; i < writers; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			payload := bytes.Repeat([]byte{byte('a' + i%26)}, 128)
			_, err := store.PutObject(ctx, "b", "same-key", bytes.NewReader(payload), PutOptions{})
			errCh <- err
		}(i)
	}
	wg.Wait()
	close(errCh)
	for err := range errCh {
		if err != nil {
			t.Fatal(err)
		}
	}

	reader, err := store.GetObject(ctx, "b", "same-key")
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	data := make([]byte, 128)
	if _, err := io.ReadFull(reader, data); err != nil {
		t.Fatal(err)
	}

	first := data[0]
	for i, c := range data {
		if c != first {
			t.Fatalf("mixed bytes at index %d: object combines multiple versions", i)
		}
	}

	info, err := store.HeadObject(ctx, "b", "same-key")
	if err != nil {
		t.Fatal(err)
	}
	if info.Size != 128 {
		t.Fatalf("size %d", info.Size)
	}

	// Blob directory must not retain garbage from overwritten versions.
	live := store.liveBlobIDs()
	if len(live) != 1 {
		t.Fatalf("expected exactly 1 live blob, got %d", len(live))
	}
}

func TestReadDuringWriteNeverPartial(t *testing.T) {
	store, err := Open(context.Background(), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ctx := context.Background()
	if err := store.CreateBucket(ctx, "b"); err != nil {
		t.Fatal(err)
	}

	original := bytes.Repeat([]byte("O"), 4096)
	if _, err := store.PutObject(ctx, "b", "k", bytes.NewReader(original), PutOptions{}); err != nil {
		t.Fatal(err)
	}

	var wg sync.WaitGroup
	for round := 0; round < 8; round++ {
		wg.Add(2)
		payload := bytes.Repeat([]byte(fmt.Sprintf("%d", round)), 4096)
		go func() {
			defer wg.Done()
			_, _ = store.PutObject(ctx, "b", "k", bytes.NewReader(payload), PutOptions{})
		}()
		go func() {
			defer wg.Done()
			r, err := store.GetObject(ctx, "b", "k")
			if err != nil {
				return
			}
			defer r.Close()
			buf := make([]byte, 4096)
			n, _ := r.Read(buf)
			first := buf[0]
			for i := 1; i < n; i++ {
				if buf[i] != first {
					t.Errorf("torn read mixing versions")
					return
				}
			}
		}()
	}
	wg.Wait()
}
