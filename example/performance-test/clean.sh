#!/usr/bin/env bash
set -euo pipefail

NAMESPACE="${NAMESPACE:-perf-test}"
HELM_RELEASE="${HELM_RELEASE:-perf-test-stack}"
NAMESPACES="${NAMESPACES:-20}"

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
bash "${SCRIPT_DIR}/down.sh"

helm uninstall "${HELM_RELEASE}" --namespace "${NAMESPACE}" --ignore-not-found --wait --timeout 3m || true

kubectl delete pvc -n "${NAMESPACE}" \
  -l "app.kubernetes.io/name in (prometheus,victorialogs)" \
  --ignore-not-found || true

kubectl delete namespace "${NAMESPACE}" --ignore-not-found || true

echo "Cleanup complete."
