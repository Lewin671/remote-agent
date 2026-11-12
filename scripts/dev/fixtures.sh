#!/bin/sh
# Generate test fixtures into the specified directory (default: ./fixtures)
set -eu
script_dir=$(cd "$(dirname "$0")" && pwd)
exec python3 "$script_dir/fixtures/generate.py" "$@"
