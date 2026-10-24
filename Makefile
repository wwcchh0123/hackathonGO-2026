.PHONY: install dev build test coverage check run clean voice voice-build voice-test

install:
	go mod download

dev:
	go run ./cmd/control

build:
	go build -o bin/control ./cmd/control

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

# Mac: brew install opus pkgconf. No Node.js or frontend build is required.
voice-build:
	pkg-config --exists opus
	CGO_ENABLED=1 go build -tags opus -o bin/voice ./cmd/voice

voice: voice-build
	./bin/voice

voice-test:
	pkg-config --exists opus
	CGO_ENABLED=1 go test -race -tags opus ./internal/linx ./internal/voice ./internal/voicecodec
