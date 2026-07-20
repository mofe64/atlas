#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
PROTO_DIR="${ROOT_DIR}/third_party/mavsdk-proto/protos"
OUT_DIR="${ROOT_DIR}/atlas-agent/internal/mavsdkpb"
MODULE="github.com/sunnyside/atlas/atlas-agent/internal/mavsdkpb"

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

command -v git >/dev/null || {
  echo "git is required" >&2
  exit 1
}

mkdir -p "${OUT_DIR}"
rm -rf "${OUT_DIR}/action" "${OUT_DIR}/camera" "${OUT_DIR}/core" "${OUT_DIR}/gimbal" "${OUT_DIR}/mission" "${OUT_DIR}/offboard" "${OUT_DIR}/telemetry" "${OUT_DIR}/mavsdk_options"
rm -f "${OUT_DIR}/mavsdk_options.pb.go"

protoc \
  -I "${PROTO_DIR}" \
  --go_out="${OUT_DIR}" \
  --go_opt=paths=source_relative \
  --go_opt=Mmavsdk_options.proto="${MODULE}/mavsdk_options" \
  --go_opt=Mcore/core.proto="${MODULE}/core" \
  --go_opt=Maction/action.proto="${MODULE}/action" \
  --go_opt=Mcamera/camera.proto="${MODULE}/camera" \
  --go_opt=Mgimbal/gimbal.proto="${MODULE}/gimbal" \
  --go_opt=Mmission/mission.proto="${MODULE}/mission" \
  --go_opt=Moffboard/offboard.proto="${MODULE}/offboard" \
  --go_opt=Mtelemetry/telemetry.proto="${MODULE}/telemetry" \
  --go-grpc_out="${OUT_DIR}" \
  --go-grpc_opt=paths=source_relative \
  --go-grpc_opt=Mmavsdk_options.proto="${MODULE}/mavsdk_options" \
  --go-grpc_opt=Mcore/core.proto="${MODULE}/core" \
  --go-grpc_opt=Maction/action.proto="${MODULE}/action" \
  --go-grpc_opt=Mcamera/camera.proto="${MODULE}/camera" \
  --go-grpc_opt=Mgimbal/gimbal.proto="${MODULE}/gimbal" \
  --go-grpc_opt=Mmission/mission.proto="${MODULE}/mission" \
  --go-grpc_opt=Moffboard/offboard.proto="${MODULE}/offboard" \
  --go-grpc_opt=Mtelemetry/telemetry.proto="${MODULE}/telemetry" \
  "${PROTO_DIR}/mavsdk_options.proto" \
  "${PROTO_DIR}/core/core.proto" \
  "${PROTO_DIR}/action/action.proto" \
  "${PROTO_DIR}/camera/camera.proto" \
  "${PROTO_DIR}/gimbal/gimbal.proto" \
  "${PROTO_DIR}/mission/mission.proto" \
  "${PROTO_DIR}/offboard/offboard.proto" \
  "${PROTO_DIR}/telemetry/telemetry.proto"

mkdir -p "${OUT_DIR}/mavsdk_options"
mv "${OUT_DIR}/mavsdk_options.pb.go" "${OUT_DIR}/mavsdk_options/mavsdk_options.pb.go"
git -C "${ROOT_DIR}/third_party/mavsdk-proto" rev-parse HEAD > "${OUT_DIR}/schema.commit"
