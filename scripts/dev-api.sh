#!/bin/sh

set -eu

project_root=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
binary_directory="$project_root/.local/bin"
binary_path="$binary_directory/email-workspace"

mkdir -p "$binary_directory"

cd "$project_root"
go build -o "$binary_path" ./cmd/email-workspace

exec "$binary_path"
