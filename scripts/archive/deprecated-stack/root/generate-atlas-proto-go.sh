#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
PROTO_DIR="${ROOT_DIR}/proto"
PROTO_FILE="${PROTO_DIR}/atlas/vehicle_agent_channel.proto"

BACKEND_OUT_DIR="${ROOT_DIR}/atlas-backend-deprecated/internal/transport/vehicleagentchannelpb"
AGENT_OUT_DIR="${ROOT_DIR}/atlas-agent-deprecated/internal/transport/vehicleagentchannelpb"

export PATH="$(go env GOPATH)/bin:${PATH}"

command -v protoc >/dev/null || {
  echo "protoc is required" >&2
  exit 1
}

command -v protoc-gen-go >/dev/null || {
  echo "protoc-gen-go is required: go install google.golang.org/protobuf/cmd/protoc-gen-go@latest" >&2
  exit 1
}

command -v protoc-gen-go-grpc >/dev/null || {
  echo "protoc-gen-go-grpc is required: go install google.golang.org/grpc/cmd/protoc-gen-go-grpc@latest" >&2
  exit 1
}

generate() {
  local out_dir="$1"

  rm -rf "${out_dir}"
  mkdir -p "${out_dir}"

  protoc \
    -I "${PROTO_DIR}" \
    --go_out="${out_dir}" \
    --go_opt=paths=source_relative \
    --go-grpc_out="${out_dir}" \
    --go-grpc_opt=paths=source_relative \
    "${PROTO_FILE}"
}

generate "${BACKEND_OUT_DIR}"
generate "${AGENT_OUT_DIR}"
