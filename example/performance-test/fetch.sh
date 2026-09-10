#!/usr/bin/env bash
set -euo pipefail

NAMESPACE="${NAMESPACE:-perf-test}"
NAMESPACES="${NAMESPACES:-20}"
JOBS="${JOBS:-20}"
LOGS="${LOGS:-125000}"

# Optional: restrict to a single namespace number passed as first argument
NS_FILTER="${1:-}"

EXPECTED_NS=$(( JOBS * LOGS ))
VL_ENDPOINT="${VL_ENDPOINT:-http://victorialogs-http.${NAMESPACE}.svc.cluster.local:9428}"

PASS=0
FAIL=0
TOTAL_ACTUAL=0

if [[ -n "${NS_FILTER}" ]]; then
  NS_START="${NS_FILTER}"
  NS_END="${NS_FILTER}"
else
  NS_START=1
  NS_END="${NAMESPACES}"
fi

for i in $(seq "${NS_START}" "${NS_END}"); do
  QUERY="name:~\"ns-${i}-job-.*\" | stats count() as total"
  ACTUAL=$(curl -sfG "${VL_ENDPOINT}/select/logsql/stats_query" \
    --data-urlencode "query=${QUERY}" \
    --data-urlencode "time=now" 2>/dev/null \
    | jq -r '[.data.result[] | select(.metric.__name__ == "total") | .value[1] | tonumber] | add // 0' \
    || echo 0)

  PCT=$(awk "BEGIN { printf \"%.1f\", ${ACTUAL}/${EXPECTED_NS}*100 }")

  (( TOTAL_ACTUAL += ACTUAL )) || true

  if [[ "${ACTUAL}" -ge "${EXPECTED_NS}" ]]; then
    printf "\033[32mPASS\033[0m  ns-%-3s  %s / %s  (%s%%)\n" \
      "${i}" "$(printf "%'d" "${ACTUAL}")" "$(printf "%'d" "${EXPECTED_NS}")" "${PCT}"
    (( PASS++ )) || true
  else
    printf "\033[33mIN PROGRESS\033[0m  ns-%-3s  %s / %s  (%s%%)\n" \
      "${i}" "$(printf "%'d" "${ACTUAL}")" "$(printf "%'d" "${EXPECTED_NS}")" "${PCT}"
    (( FAIL++ )) || true
  fi
done

NS_COUNT=$(( NS_END - NS_START + 1 ))
TOTAL_EXPECTED=$(( NS_COUNT * JOBS * LOGS ))
TOTAL_PCT=$(awk "BEGIN { printf \"%.1f\", ${TOTAL_ACTUAL}/${TOTAL_EXPECTED}*100 }")

echo ""
printf "Summary: \033[32m%d complete\033[0m, \033[33m%d in progress\033[0m\n" "${PASS}" "${FAIL}"
printf "Total:   %s / %s  (%s%%)\n" \
  "$(printf "%'d" "${TOTAL_ACTUAL}")" "$(printf "%'d" "${TOTAL_EXPECTED}")" "${TOTAL_PCT}"
