#!/usr/bin/env bash

set -euo pipefail

profile_path="${1:-cover.out}"
required="${2:-100.0}"

total="$(go tool cover -func="${profile_path}" | awk '/^total:/ {gsub("%","",$3); print $3}')"

if [[ -z "${total}" ]]; then
  echo "unable to read total coverage from ${profile_path}" >&2
  exit 1
fi

if [[ "${total}" != "${required}" ]]; then
  echo "coverage check failed: expected ${required}% and got ${total}%" >&2
  exit 1
fi

echo "coverage check passed: ${total}%"
