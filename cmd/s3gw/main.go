package main

import (
	"context"
	"flag"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/guoyyit/s3-object-gateway-go/internal/auth"
	"github.com/guoyyit/s3-object-gateway-go/internal/server"
	"github.com/guoyyit/s3-object-gateway-go/internal/storage"
)

func main() {
	if err := run(); err != nil {
		log.Fatal(err)
	}
}

func run() error {
	addr := flag.String("addr", "127.0.0.1:8333", "listen address")
	dataDir := flag.String("data", "./data", "on-disk data directory")
	accessKey := flag.String("access-key", envOr("S3_ACCESS_KEY", "testaccesskey"), "S3 access key id")
	secretKey := flag.String("secret-key", envOr("S3_SECRET_KEY", "testsecretkey"), "S3 secret access key")
	abandonedAge := flag.Duration("abandoned-upload-age", 24*time.Hour, "age after which multipart uploads are reclaimed")
	flag.Parse()

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	store, err := storage.Open(ctx, *dataDir)
	if err != nil {
		return err
	}
	defer store.Close()

	creds := []auth.Credentials{{AccessKeyID: *accessKey, SecretAccessKey: *secretKey}}
	srv := server.New(server.Config{Addr: *addr, Credentials: creds, DataDir: *dataDir}, store)

	httpServer := &http.Server{Addr: *addr, Handler: srv.Handler()}

	sweeper := time.NewTicker(time.Hour)
	defer sweeper.Stop()
	go func() {
		for {
			select {
			case <-ctx.Done():
				return
			case <-sweeper.C:
				_ = store.SweepAbandoned(context.Background(), *abandonedAge)
			}
		}
	}()

	go func() {
		log.Printf("s3 gateway listening on %s (data=%s)", *addr, *dataDir)
		if err := httpServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatal(err)
		}
	}()

	<-ctx.Done()
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return httpServer.Shutdown(shutdownCtx)
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
