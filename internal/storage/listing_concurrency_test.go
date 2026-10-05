package storage

import (
	"bytes"
	"context"
	"fmt"
	"sync"
	"testing"
)

// TestListTokenStaysUsableUnderChurn verifies that each listing page is a
// strictly ordered snapshot slice and that continuation tokens remain
// usable while concurrent writes and deletes mutate the bucket.
func TestListTokenStaysUsableUnderChurn(t *testing.T) {
	store, err := Open(context.Background(), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ctx := context.Background()
	if err := store.CreateBucket(ctx, "b"); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 237; i++ {
		key := fmt.Sprintf("key-%04d", i)
		if _, err := store.PutObject(ctx, "b", key, bytes.NewReader([]byte("v")), PutOptions{}); err != nil {
			t.Fatal(err)
		}
	}

	stop := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		i := 500
		for {
			select {
			case <-stop:
				return
			default:
				_, _ = store.PutObject(ctx, "b", fmt.Sprintf("extra-%04d", i), bytes.NewReader([]byte("v")), PutOptions{})
				_ = store.DeleteObject(ctx, "b", fmt.Sprintf("key-%04d", i%237))
				i++
			}
		}
	}()

	checkPage := func(objs []ObjectInfo, token string) {
		t.Helper()
		for i := 1; i < len(objs); i++ {
			if objs[i].Key <= objs[i-1].Key {
				t.Fatalf("page not strictly ordered: %q <= %q", objs[i].Key, objs[i-1].Key)
			}
		}
		if token != "" {
			for _, info := range objs {
				if info.Key <= token {
					t.Fatalf("token %q failed to exclude key %q", token, info.Key)
				}
			}
		}
	}

	for attempt := 0; attempt < 50; attempt++ {
		res, err := store.ListObjects(ctx, "b", ListOptions{MaxKeys: 7})
		if err != nil {
			t.Fatal(err)
		}
		checkPage(res.Objects, "")
		if res.IsTruncated {
			token := res.NextContinuationToken
			res2, lerr := store.ListObjects(ctx, "b", ListOptions{MaxKeys: 7, ContinuationToken: token})
			if lerr != nil {
				t.Fatal(lerr)
			}
			checkPage(res2.Objects, token)
		}
	}

	close(stop)
	wg.Wait()
}
