.PHONY: install dev build test check run clean

install:
	go mod download

dev:
	go run ./cmd/control

build:
	go build -o bin/control ./cmd/control

test:
	go test -race ./...

check:
	test -z "$$(gofmt -l cmd internal)"
	go vet ./...

run:
	./bin/control

clean:
	rm -rf bin
