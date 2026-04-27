#!/usr/bin/env bash
set -euo pipefail

auth_dir="${CODEX_AUTH_DIR:-/home/ubuntu/CLIProxyAPI/auths}"

if [[ ! -d "$auth_dir" ]]; then
  echo "codex auth dir not found: $auth_dir" >&2
  exit 2
fi

violations=0
total=0
plus=0
team=0

shopt -s nullglob
for path in "$auth_dir"/codex-*.json; do
  total=$((total + 1))
  file="${path##*/}"
  case "$file" in
    *-plus.json)
      plus=$((plus + 1))
      ;;
    *-team.json)
      team=$((team + 1))
      ;;
    *-free.json)
      echo "forbidden free Codex auth file: $file" >&2
      violations=$((violations + 1))
      ;;
    *)
      echo "unknown Codex auth plan suffix: $file" >&2
      violations=$((violations + 1))
      ;;
  esac
done

echo "codex_auth_total=$total plus=$plus team=$team forbidden_or_unknown=$violations"

if (( violations > 0 )); then
  exit 1
fi
