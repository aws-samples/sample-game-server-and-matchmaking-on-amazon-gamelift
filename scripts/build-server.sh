#!/usr/bin/env bash
# Builds the game server for a managed Amazon EC2 fleet: a Linux arm64 binary (for AWS Graviton instances) in
# server/build, which `npx cdk deploy -c fleetType=ec2` uploads as the fleet's build.
set -euo pipefail
ROOT="$(cd "$(dirname "$0")/.." && pwd)"
rm -rf "$ROOT/server/build"
mkdir -p "$ROOT/server/build"
(cd "$ROOT/server" && CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build -trimpath -o build/server ./cmd/server)
# The upload keeps the file's execute permission, and the server creates its own log folder, so the build
# needs no install script.
echo "Built $ROOT/server/build/server"
