.PHONY: install dev build test coverage check run clean

install:
	go mod download

dev:
	go run ./cmd/control

build:
	go build -o bin/control ./cmd/control
	go build -o bin/device-display ./cmd/device-display

test:
	go test -race ./...

coverage:
	go test -count=1 -covermode=atomic -coverprofile=coverage.out ./...
	go tool cover -func=coverage.out

check:
	test -z "$$(gofmt -l cmd internal)"
	go vet ./...

run:
	./bin/control

clean:
	rm -rf bin
