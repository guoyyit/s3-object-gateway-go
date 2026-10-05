.PHONY: build test vet acceptance run

build:
	go build ./...

vet:
	go vet ./...

test:
	go test -race -count=1 ./...

acceptance: vet test

run: build
	go run ./cmd/s3gw -addr 127.0.0.1:8333 -data ./data
