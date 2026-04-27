#!/usr/bin/env bash
set -euo pipefail
cd "$(dirname "$0")/../frontend"
mkdir -p ../tmp/logs
bun install
exec bun run dev "$@" 2>&1 | tee ../tmp/logs/frontend-dev.log
