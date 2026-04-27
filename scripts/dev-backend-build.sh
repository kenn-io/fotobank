#!/usr/bin/env bash
set -euo pipefail
mkdir -p tmp
go build -o tmp/fotobank ./cmd/fotobank
