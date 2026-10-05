.PHONY: build test vet accept run fmt

build:
	go build ./...

fmt:
	gofmt -w .

vet:
	go vet ./...

test:
	go test -count=1 ./...

# Executable acceptance command: build, vet and run all tests with the race detector.
accept:
	go build ./... && go vet ./... && go test -race -count=1 ./...

run: build
	go run ./cmd/server -addr :9000 -data ./.s3gw-data
