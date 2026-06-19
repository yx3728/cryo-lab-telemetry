#!/usr/bin/env bash
# Generate N throwaway bench source/token pairs.
#   - tokens.local.json : [{source,token}] consumed by `bench producers`
#   - bench_tokens.env  : EXTRA_INGEST_TOKENS=",bench0:..,bench1:.." for the box
# Both are git-ignored. Values are NOT printed.
set -euo pipefail
N="${1:-50}"
DIR="$(cd "$(dirname "$0")" && pwd)"
JSON="$DIR/tokens.local.json"
ENVF="$DIR/bench_tokens.env"

json="["
extra=""
for i in $(seq 0 $((N - 1))); do
  tok=$(openssl rand -hex 12)
  src="bench$i"
  extra="${extra},${src}:${tok}"
  sep=","; [ "$i" -eq 0 ] && sep=""
  json="${json}${sep}{\"source\":\"${src}\",\"token\":\"${tok}\"}"
done
json="${json}]"

printf '%s\n' "$json" > "$JSON"
printf 'EXTRA_INGEST_TOKENS=%s\n' "$extra" > "$ENVF"
echo "generated $N bench tokens -> $JSON, $ENVF (values hidden)"
