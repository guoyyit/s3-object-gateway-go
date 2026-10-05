// Command s3gw runs a local S3-compatible object storage server.
package main

import (
	"context"
	"flag"
	"log"
	"net/http"
	"os"
	"time"

	"github.com/guoyyit/s3-object-gateway-go/internal/api"
	"github.com/guoyyit/s3-object-gateway-go/internal/auth"
	"github.com/guoyyit/s3-object-gateway-go/internal/storage"
)

func main() {
	addr := flag.String("addr", envOr("S3GW_ADDR", ":9000"), "listen address")
	dataDir := flag.String("data", envOr("S3GW_DATA", "./.s3gw-data"), "data directory")
	accessKey := flag.String("access-key", envOr("S3GW_ACCESS_KEY", "testaccesskey"), "access key id")
	secretKey := flag.String("secret-key", envOr("S3GW_SECRET_KEY", "testsecretkey"), "secret access key")
	flag.Parse()

	store, err := storage.Open(*dataDir)
	if err != nil {
		log.Fatalf("open store: %v", err)
	}
	defer store.Close()

	// Reclaim multipart uploads abandoned for more than a day. Parts left by
	// an aborted, timed-out or crashed client are removed on startup and
	// periodically thereafter.
	go runSweeper(store)

	verifier := auth.NewVerifier(func(_ context.Context, key string) (string, bool, error) {
		if key == *accessKey {
			return *secretKey, true, nil
		}
		return "", false, nil
	})

	srv := &http.Server{
		Addr:              *addr,
		Handler:           api.NewServer(store, verifier).Handler(),
		ReadHeaderTimeout: 30 * time.Second,
	}

	log.Printf("s3gw listening on %s, data at %s", *addr, *dataDir)
	if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		log.Fatal(err)
	}
}

func runSweeper(store *storage.Store) {
	ticker := time.NewTicker(time.Hour)
	defer ticker.Stop()
	for range ticker.C {
		cutoff := time.Now().Add(-24 * time.Hour)
		_ = store.SweepAbandonedUploads(context.Background(), cutoff)
	}
}

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}
