#!/usr/bin/env bash
set -euo pipefail

NAMESPACE="${NAMESPACE:-perf-test}"
NAMESPACES="${NAMESPACES:-20}"
JOBS="${JOBS:-20}"
LOGS="${LOGS:-50000}"
LOGS_DELAY="${LOGS_DELAY:-10ms}"
LOGGER_IMAGE="${LOGGER_IMAGE:-nickytd/log-generator:v0.1.10}"
NODE_SELECTOR="${NODE_SELECTOR:-}"

echo "Starting load test: ${NAMESPACES} namespaces x ${JOBS} jobs x ${LOGS} logs (delay: ${LOGS_DELAY})"
echo "Logger image: ${LOGGER_IMAGE}"
[[ -n "${NODE_SELECTOR}" ]] && echo "Node selector: ${NODE_SELECTOR}"

# Build nodeSelector YAML block from KEY=VALUE pairs (comma-separated)
node_selector_yaml=""
if [[ -n "${NODE_SELECTOR}" ]]; then
  node_selector_yaml="      nodeSelector:"$'\n'
  IFS=',' read -ra pairs <<< "${NODE_SELECTOR}"
  for pair in "${pairs[@]}"; do
    key="${pair%%=*}"; val="${pair#*=}"
    node_selector_yaml+="        ${key}: ${val}"$'\n'
  done
fi

pids=()

for i in $(seq 1 "${NAMESPACES}"); do
  ns="perf-ns-${i}"
  if ! kubectl get namespace "${ns}" &>/dev/null; then
    kubectl create namespace "${ns}" &
    pids+=($!)
  fi
done

for pid in "${pids[@]}"; do
  wait "${pid}" || { echo "ERROR: failed to create namespace (pid ${pid})"; exit 1; }
done

pids=()

for i in $(seq 1 "${NAMESPACES}"); do
  ns="perf-ns-${i}"
  for j in $(seq 1 "${JOBS}"); do
    LOGGER_NAME="ns-${i}-job-${j}"
    kubectl apply -f - &>/dev/null <<EOF &
apiVersion: batch/v1
kind: Job
metadata:
  name: logger-${j}
  namespace: ${ns}
  labels:
    app.kubernetes.io/name: logger
    perf-test/ns: ns-${i}
    perf-test/job: logger-${j}
spec:
  completions: 1
  parallelism: 1
  ttlSecondsAfterFinished: 7200
  template:
    metadata:
      labels:
        app.kubernetes.io/name: logger
        perf-test/ns: ns-${i}
        perf-test/job: logger-${j}
    spec:
      restartPolicy: Never
${node_selector_yaml}      topologySpreadConstraints:
        - maxSkew: 1
          topologyKey: kubernetes.io/hostname
          whenUnsatisfiable: ScheduleAnyway
          labelSelector:
            matchLabels:
              app.kubernetes.io/name: logger
      containers:
        - name: logger
          image: ${LOGGER_IMAGE}
          args:
            - --count=${LOGS}
            - --wait=${LOGS_DELAY}
            - --name=${LOGGER_NAME}
            - --json
EOF
    pids+=($!)
  done
done

rc=0
for pid in "${pids[@]}"; do
  wait "${pid}" || rc=$?
done
if [[ "${rc}" -ne 0 ]]; then
  echo "ERROR: one or more kubectl apply commands failed"
  exit "${rc}"
fi

echo "Logger jobs started: ${NAMESPACES} namespaces x ${JOBS} jobs x ${LOGS} logs"
echo "Expected total: $(( NAMESPACES * JOBS * LOGS )) log lines"
