#!/usr/bin/env bash
set -euo pipefail
mkdir -p tmp
go build -tags sqlite_fts5 -o tmp/fotobank ./cmd/fotobank
