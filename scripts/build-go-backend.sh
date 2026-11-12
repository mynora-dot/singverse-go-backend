#!/bin/sh
set -eu
cd "$(dirname "$0")/.."
. ./scripts/go-env.sh
test -s internal/web/dist/admin/index.html
test -s internal/web/dist/party/index.html
test -s internal/web/dist/downloads/index.html
version=$(cat VERSION)
case "$version" in ''|*[!a-zA-Z0-9._+-]*) echo 'Invalid VERSION' >&2; exit 1;; esac
mkdir -p bin
CGO_ENABLED=0 go build -trimpath -ldflags "-s -w -X main.version=$version" -o bin/singverse ./cmd/singverse
