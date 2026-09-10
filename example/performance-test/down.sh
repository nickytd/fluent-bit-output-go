#!/usr/bin/env bash
set -euo pipefail

NAMESPACES="${NAMESPACES:-20}"

echo "Deleting ${NAMESPACES} test namespaces..."

pids=()
for i in $(seq 1 "${NAMESPACES}"); do
  kubectl delete namespace "perf-ns-${i}" --ignore-not-found &
  pids+=($!)
done

for pid in "${pids[@]}"; do
  wait "${pid}"
done

echo "All test namespaces deleted."
