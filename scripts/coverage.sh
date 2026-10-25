#!/bin/sh
# Report both builds; do not remove source files from either profile.
set -eu

go test -count=1 -timeout=120s -covermode=atomic -coverprofile=coverage.out ./...
go tool cover -func=coverage.out | tail -n 1

pkg-config --exists opus
CGO_ENABLED=1 go test -count=1 -timeout=120s -tags opus -covermode=atomic \
  -coverpkg=./internal/... -coverprofile=coverage-production.out ./...
go tool cover -func=coverage-production.out | tail -n 1

# -coverpkg profiles can contain the same block from multiple test binaries.
# Merge by block address before counting statements, like go tool cover does.
awk '
  NR == 1 { next }
  { statements[$1] = $2; hits[$1] += $3 }
  END {
    for (block in statements) {
      total += statements[block]
      if (hits[block] > 0) covered += statements[block]
    }
    if (!total) { print "No production coverage data"; exit 1 }
    percentage = covered * 100 / total
    printf "Production internal packages (Opus, cross-package): %d/%d statements, %.2f%%; required >=90%%\n", covered, total, percentage
    print "Includes Mock, HTTP/MCP, voice, Opus and display handlers; cmd/* and browser JS are outside this Go metric."
    if (percentage < 90) exit 1
  }
' coverage-production.out
