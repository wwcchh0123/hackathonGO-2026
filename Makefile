.PHONY: install dev build test coverage check run clean voice voice-build voice-test

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
	sh scripts/coverage.sh

check:
	test -z "$$(gofmt -l cmd internal)"
	go vet ./...
	go run honnef.co/go/tools/cmd/staticcheck@v0.7.0 ./...

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
