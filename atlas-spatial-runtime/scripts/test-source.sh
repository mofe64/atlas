#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
SPATIAL_DIR="$(cd "${SCRIPT_DIR}/.." && pwd)"
export PYTHONPATH="${SPATIAL_DIR}/src${PYTHONPATH:+:${PYTHONPATH}}"
export PYTHONDONTWRITEBYTECODE=1
python3 -m unittest discover -s "${SPATIAL_DIR}/tests" -p 'test_*.py' -v
