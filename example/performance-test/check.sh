#!/usr/bin/env bash
set -euo pipefail

NAMESPACE="${NAMESPACE:-perf-test}"
NAMESPACES="${NAMESPACES:-20}"
JOBS="${JOBS:-20}"
LOGS="${LOGS:-125000}"

EXPECTED=$(( NAMESPACES * JOBS * LOGS ))
VL_ENDPOINT="${VL_ENDPOINT:-http://victorialogs-http.${NAMESPACE}.svc.cluster.local:9428}"

QUERY='name:~"ns-.*" | stats count() as total'

RESPONSE=$(curl -sfG "${VL_ENDPOINT}/select/logsql/stats_query" \
  --data-urlencode "query=${QUERY}" \
  --data-urlencode "time=now" 2>/dev/null) || {
  echo "ERROR: no response from VictoriaLogs at ${VL_ENDPOINT}"
  exit 1
}

ACTUAL=$(jq -r '[.data.result[] | select(.metric.__name__ == "total") | .value[1] | tonumber] | add // 0' \
  <<< "${RESPONSE}" 2>/dev/null || echo 0)

PCT=$(awk "BEGIN { printf \"%.1f\", ${ACTUAL}/${EXPECTED}*100 }")

if [[ "${ACTUAL}" -ge "${EXPECTED}" ]]; then
  STATUS="\033[32mPASS\033[0m"
else
  STATUS="\033[33mIN PROGRESS\033[0m"
fi

printf "${STATUS}  %s / %s  (%s%%)\n" \
  "$(printf "%'d" "${ACTUAL}")" \
  "$(printf "%'d" "${EXPECTED}")" \
  "${PCT}"
