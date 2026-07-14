#!/bin/sh
set -eu

repository_root=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
database_path=${ATLAS_SQLITE_PATH:-"$repository_root/.atlas-run/native-dev/atlas.db"}

case "$database_path" in
  /*) ;;
  *)
    printf '%s\n' "ATLAS_SQLITE_PATH must be absolute: $database_path" >&2
    exit 2
    ;;
esac

mkdir -p "$(dirname -- "$database_path")"
cd "$repository_root/atlas"
exec env ATLAS_SQLITE_PATH="$database_path" npm run tauri -- dev "$@"
