#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
NAMESPACE="${NAMESPACE:-perf-test}"
HELM_RELEASE="${HELM_RELEASE:-perf-test-stack}"
VALUES_FILE="${VALUES_FILE:-}"

# Auto-detect local values.yaml next to this script if not explicitly set
if [[ -z "${VALUES_FILE}" && -f "${SCRIPT_DIR}/values.yaml" ]]; then
  VALUES_FILE="${SCRIPT_DIR}/values.yaml"
  echo "Using local values file: ${VALUES_FILE}"
fi

HELM_ARGS=(
  upgrade --install "${HELM_RELEASE}"
  "${SCRIPT_DIR}/charts/perf-test-stack"
  --namespace "${NAMESPACE}"
  --create-namespace
  --wait
  --timeout 5m
)

if [[ -n "${VALUES_FILE}" ]]; then
  HELM_ARGS+=(--values "${VALUES_FILE}")
fi

helm "${HELM_ARGS[@]}"

echo "Setup complete — stack deployed to namespace: ${NAMESPACE}"
