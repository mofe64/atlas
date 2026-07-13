#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
PROTO_DIR="${ROOT_DIR}/proto"
PROTO_FILE="${PROTO_DIR}/atlas/ground_station.proto"
AGENT_MODULE="github.com/sunnyside/atlas/atlas-agent"

export PATH="$(go env GOPATH)/bin:${PATH}"

command -v protoc >/dev/null || {
  echo "protoc is required" >&2
  exit 1
}
command -v protoc-gen-go >/dev/null || {
  echo "protoc-gen-go is required" >&2
  exit 1
}
command -v protoc-gen-go-grpc >/dev/null || {
  echo "protoc-gen-go-grpc is required" >&2
  exit 1
}

rm -rf "${ROOT_DIR}/atlas-agent/internal/transport/groundstationpb"

protoc \
  -I "${PROTO_DIR}" \
  --go_out="${ROOT_DIR}/atlas-agent" \
  --go_opt="module=${AGENT_MODULE}" \
  --go-grpc_out="${ROOT_DIR}/atlas-agent" \
  --go-grpc_opt="module=${AGENT_MODULE}" \
  "${PROTO_FILE}"
