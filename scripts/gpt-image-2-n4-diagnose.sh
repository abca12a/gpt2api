#!/usr/bin/env bash
set -euo pipefail

script_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
repo_root="$(cd "$script_dir/.." && pwd)"

mode="${GPT2API_LIVE_SINGLE_ACCOUNT_N4_MODE:-single}"
rounds="${GPT2API_LIVE_SINGLE_ACCOUNT_N4_ROUNDS:-1}"
account_id="${GPT2API_LIVE_ACCOUNT_ID:-}"
jsonl_out=""
log_out=""

usage() {
  cat <<'EOF'
Usage: scripts/gpt-image-2-n4-diagnose.sh [options]

Options:
  --mode single|parallel   single=runOnce(N=4), parallel=Runner.Run(N=4)
  --rounds N               repeat count, default 1
  --account-id ID          lock other free accounts and force this account
  --jsonl-out PATH         write GPT2API_IMAGE_N4_DIAGNOSTIC_JSON lines as JSONL
  --log-out PATH           also save raw go test output
  -h, --help               show help

Environment overrides:
  GPT2API_TEST_MYSQL_DSN
  GPT2API_TEST_AES_KEY
  GPT2API_TEST_REDIS_ADDR
EOF
}

while [[ $# -gt 0 ]]; do
  case "$1" in
    --mode)
      mode="${2:-}"
      shift 2
      ;;
    --rounds)
      rounds="${2:-}"
      shift 2
      ;;
    --account-id)
      account_id="${2:-}"
      shift 2
      ;;
    --jsonl-out)
      jsonl_out="${2:-}"
      shift 2
      ;;
    --log-out)
      log_out="${2:-}"
      shift 2
      ;;
    -h|--help)
      usage
      exit 0
      ;;
    *)
      echo "unknown option: $1" >&2
      usage >&2
      exit 2
      ;;
  esac
done

case "$mode" in
  single|single_run_once)
    mode="single"
    ;;
  parallel|merge|runner|parallel_merge)
    mode="parallel"
    ;;
  *)
    echo "invalid --mode: $mode" >&2
    exit 2
    ;;
esac

if ! [[ "$rounds" =~ ^[0-9]+$ ]] || [[ "$rounds" -le 0 ]]; then
  echo "invalid --rounds: $rounds" >&2
  exit 2
fi

cd "$repo_root"

if [[ -z "${GPT2API_TEST_MYSQL_DSN:-}" ]]; then
  raw_dsn="$(docker exec gpt2api-server printenv GPT2API_MYSQL_DSN)"
  export GPT2API_TEST_MYSQL_DSN="${raw_dsn/@tcp(mysql:3306)/@tcp(127.0.0.1:3306)}"
fi
if [[ -z "${GPT2API_TEST_AES_KEY:-}" ]]; then
  export GPT2API_TEST_AES_KEY="$(docker exec gpt2api-server printenv GPT2API_CRYPTO_AES_KEY)"
fi
export GPT2API_TEST_REDIS_ADDR="${GPT2API_TEST_REDIS_ADDR:-127.0.0.1:6379}"
export GPT2API_LIVE_SINGLE_ACCOUNT_N4=1
export GPT2API_LIVE_SINGLE_ACCOUNT_N4_ROUNDS="$rounds"
export GPT2API_LIVE_SINGLE_ACCOUNT_N4_MODE="$mode"
if [[ -n "$account_id" ]]; then
  export GPT2API_LIVE_ACCOUNT_ID="$account_id"
fi

tmp_log=""
log_file=""
if [[ -n "$log_out" ]]; then
  mkdir -p "$(dirname "$log_out")"
  log_file="$log_out"
else
  tmp_log="$(mktemp)"
  log_file="$tmp_log"
fi

set +e
go test ./internal/image -run TestLiveSingleAccountN4 -count=1 -v 2>&1 | tee "$log_file"
status="${PIPESTATUS[0]}"
set -e

if [[ -n "$jsonl_out" ]]; then
  mkdir -p "$(dirname "$jsonl_out")"
  grep 'GPT2API_IMAGE_N4_DIAGNOSTIC_JSON=' "$log_file" |
    sed 's/^.*GPT2API_IMAGE_N4_DIAGNOSTIC_JSON=//' >"$jsonl_out" || true
fi

if [[ -n "$tmp_log" && "$log_file" == "$tmp_log" ]]; then
  rm -f "$tmp_log"
fi

exit "$status"
