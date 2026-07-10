#!/usr/bin/env bash

set -euo pipefail

root_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
tmp_dir="$(mktemp -d)"
trap 'rm -rf "${tmp_dir}"' EXIT

for tool in gitleaks strings awk; do
  if ! command -v "${tool}" >/dev/null 2>&1; then
    echo "required command not found: ${tool}" >&2
    exit 1
  fi
done

archives=("${root_dir}"/lib/*.a)
if [[ ! -e "${archives[0]}" ]]; then
  echo "no static library artifacts found" >&2
  exit 1
fi

for archive in "${archives[@]}"; do
  output="${tmp_dir}/$(basename "${archive}").strings"
  # Keep unrelated binary strings from forming a synthetic key/value match.
  strings "${archive}" |
    awk '{print $0 " __GITLEAKS_STRING_BOUNDARY__"}' > "${output}"
done

gitleaks dir "${tmp_dir}" --redact --no-banner
