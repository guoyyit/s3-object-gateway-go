package storage

import (
	"bytes"
	"context"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestRestartPreservesObjects(t *testing.T) {
	dir := t.TempDir()
	store, err := Open(context.Background(), dir)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if err := store.CreateBucket(ctx, "persist"); err != nil {
		t.Fatal(err)
	}
	if _, err := store.PutObject(ctx, "persist", "keep", bytes.NewReader([]byte("durable")), PutOptions{}); err != nil {
		t.Fatal(err)
	}

	up, err := store.CreateMultipartUpload(ctx, "persist", "pending", PutOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}

	store2, err := Open(context.Background(), dir)
	if err != nil {
		t.Fatal(err)
	}
	defer store2.Close()

	reader, err := store2.GetObject(ctx, "persist", "keep")
	if err != nil {
		t.Fatalf("object lost after restart: %v", err)
	}
	data := make([]byte, 7)
	if _, err := io.ReadFull(reader, data); err != nil {
		t.Fatal(err)
	}
	reader.Close()
	if string(data) != "durable" {
		t.Fatalf("data %q", data)
	}

	// Pending upload is recognized and can be continued.
	ups, err := store2.ListMultipartUploads(ctx, "persist")
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, u := range ups {
		if u.UploadID == up.UploadID {
			found = true
		}
	}
	if !found {
		t.Fatal("pending multipart upload not recovered")
	}

	// Abort after restart cleans it up.
	if err := store2.AbortMultipartUpload(ctx, "persist", "pending", up.UploadID); err != nil {
		t.Fatal(err)
	}
	if _, err := store2.HeadObject(ctx, "persist", "pending"); err == nil {
		t.Fatal("aborted in-progress upload must not become an object")
	}
}

func TestSweepAbandoned(t *testing.T) {
	dir := t.TempDir()
	store, err := Open(context.Background(), dir)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ctx := context.Background()
	if err := store.CreateBucket(ctx, "b"); err != nil {
		t.Fatal(err)
	}
	up, err := store.CreateMultipartUpload(ctx, "b", "old", PutOptions{})
	if err != nil {
		t.Fatal(err)
	}

	// Force the upload to look old.
	store.mu.Lock()
	if st, ok := store.uploads[uploadKey("b", up.UploadID)]; ok {
		st.Initiated = time.Now().Add(-48 * time.Hour)
		persisted := copyUploadState(st)
		_ = store.saveUploadState(persisted)
	}
	store.mu.Unlock()

	if err := store.SweepAbandoned(ctx, time.Hour); err != nil {
		t.Fatal(err)
	}
	if _, err := store.getUpload("b", up.UploadID); err == nil {
		t.Fatal("stale upload was not reclaimed")
	}
	if _, err := os.Stat(store.uploadDir("b", up.UploadID)); !os.IsNotExist(err) {
		t.Fatalf("upload directory not removed: %v", err)
	}
}

func TestCorruptMetadataNotExposed(t *testing.T) {
	dir := t.TempDir()
	store, err := Open(context.Background(), dir)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if err := store.CreateBucket(ctx, "b"); err != nil {
		t.Fatal(err)
	}
	if _, err := store.PutObject(ctx, "b", "good", bytes.NewReader([]byte("ok")), PutOptions{}); err != nil {
		t.Fatal(err)
	}
	store.Close()

	// Delete the blob backing "good" to simulate corruption, then reopen:
	// the metadata must be ignored rather than exposing a broken object.
	blobRoot := filepath.Join(dir, "blobs")
	err = filepath.WalkDir(blobRoot, func(path string, d os.DirEntry, walkErr error) error {
		if walkErr != nil || d.IsDir() {
			return nil
		}
		if len(d.Name()) == 32 {
			return os.Remove(path)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}

	store2, err := Open(context.Background(), dir)
	if err != nil {
		t.Fatal(err)
	}
	defer store2.Close()
	if _, err := store2.HeadObject(ctx, "b", "good"); err == nil {
		t.Fatal("corrupt object must not be exposed")
	}
}
