#!/bin/sh
# Code quality gates — run before pushing.
# Usage: ./check.sh [--fix]
set -eu

repo_root=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
exec "$repo_root/scripts/verify" "$@"
