NAMESPACE    ?= perf-test
HELM_RELEASE ?= fluent-bit-plugin
NAMESPACES   ?= 20
JOBS         ?= 20
LOGS         ?= 125000
LOGS_DELAY   ?= 10ms
LOGGER_IMAGE ?= nickytd/log-generator:v0.1.10
NS           ?=

export NAMESPACE HELM_RELEASE NAMESPACES JOBS LOGS LOGS_DELAY LOGGER_IMAGE VALUES_FILE

SCRIPT_DIR := $(dir $(realpath $(lastword $(MAKEFILE_LIST))))

VALUES_FILE ?=

.PHONY: setup run check fetch down clean test \
        port-forward-prometheus port-forward-grafana port-forward-victorialogs

setup:
	@bash $(SCRIPT_DIR)setup.sh

run:
	@bash $(SCRIPT_DIR)run.sh

check:
	@bash $(SCRIPT_DIR)check.sh

fetch:
	@bash $(SCRIPT_DIR)fetch.sh $(NS)

down:
	@bash $(SCRIPT_DIR)down.sh

clean:
	@bash $(SCRIPT_DIR)clean.sh

test: setup run check

port-forward-prometheus:
	kubectl port-forward -n $(NAMESPACE) svc/prometheus 9090:9090

port-forward-grafana:
	kubectl port-forward -n $(NAMESPACE) svc/grafana 3000:3000

port-forward-victorialogs:
	kubectl port-forward -n $(NAMESPACE) svc/victorialogs-http 9428:9428
