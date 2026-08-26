# SPDX-FileCopyrightText: 2026 nickytd
# SPDX-License-Identifier: Apache-2.0
PLUGIN_NAME  := go-out
ROOT_DIR     := $(shell dirname $(realpath $(lastword $(MAKEFILE_LIST))))
SRC_DIRS     := $(shell go list -f '{{.Dir}}' $(ROOT_DIR)/...)

TOOLS_DIR    := $(ROOT_DIR)/tools
TOOLS_MOD    := $(TOOLS_DIR)/go.mod
GO_TOOL      := go tool -modfile=$(TOOLS_MOD)

# plugin.go uses cgo "C" pseudo-import which gci cannot handle correctly; exclude root package
GCI_DIRS     := $(ROOT_DIR)/internal $(ROOT_DIR)/cmd
GCI_OPT      ?= -s standard -s default -s "prefix($(shell go list -m))" --skip-generated
TEST_ARGS    ?=

.DEFAULT_GOAL := all

.PHONY: all
all: check unit-test build

$(ROOT_DIR)/bin/$(PLUGIN_NAME).so: $(wildcard *.go) go.mod
	@mkdir -p $(ROOT_DIR)/bin
	go build -buildmode=c-shared -o $@ .

.PHONY: build
build: $(ROOT_DIR)/bin/$(PLUGIN_NAME).so

.PHONY: tidy
tidy:
	@go mod tidy
	@cd $(TOOLS_DIR) && go mod tidy

.PHONY: gci
gci: tidy
	@echo "Running gci..."
	@$(GO_TOOL) gci write $(GCI_OPT) $(GCI_DIRS)

.PHONY: fmt
fmt: tidy
	@echo "Running fmt..."
	@$(GO_TOOL) golangci-lint fmt --config=$(ROOT_DIR)/.golangci.yaml $(SRC_DIRS)

.PHONY: check-go-fix
check-go-fix:
	@echo "Running go fix..."
	@before=$$(find . -name '*.go' -not -path './tools/*' | sort | xargs md5sum 2>/dev/null); \
	go fix $(ROOT_DIR)/...; \
	after=$$(find . -name '*.go' -not -path './tools/*' | sort | xargs md5sum 2>/dev/null); \
	if [ "$$before" != "$$after" ]; then \
		echo "Error: go fix produced changes. Please run 'go fix ./...' and commit."; \
		exit 1; \
	fi

.PHONY: lint
lint: tidy
	@echo "Running lint..."
	@$(GO_TOOL) golangci-lint run --config=$(ROOT_DIR)/.golangci.yaml $(SRC_DIRS)

.PHONY: check
check: tidy fmt gci check-go-fix lint

.PHONY: unit-test
unit-test: tidy
	@echo "Running unit tests..."
	@$(GO_TOOL) gotestsum --format-hide-empty-pkg -- $(TEST_ARGS) $(ROOT_DIR)/...

.PHONY: e2e-test
e2e-test: build
	@echo "Running e2e tests..."
	@PLUGIN_DIR=$(ROOT_DIR)/bin $(GO_TOOL) gotestsum \
		--format-hide-empty-pkg \
		-- -tags=e2e $(ROOT_DIR)/...

.PHONY: test
test: unit-test e2e-test

.PHONY: add-license-headers
add-license-headers: tidy
	@$(GO_TOOL) addlicense \
		-c "nickytd" \
		-l apache \
		-s=only \
		-y "2026" \
		-ignore "**/*.md" \
		-ignore "**/*.yaml" \
		-ignore "**/*.yml" \
		-ignore "**/Dockerfile*" \
		$(ROOT_DIR)

.PHONY: sast
sast: tidy
	@$(GO_TOOL) gosec -exclude-generated -exclude-dir=e2e $(ROOT_DIR)/...

.PHONY: govulncheck
govulncheck: tidy
	@$(GO_TOOL) govulncheck $(ROOT_DIR)/...

.PHONY: clean
clean:
	@echo "Cleaning..."
	@rm -rf $(ROOT_DIR)/bin

.PHONY: run
run: $(ROOT_DIR)/bin/$(PLUGIN_NAME).so
	@fluent-bit -c $(ROOT_DIR)/fluent-bit.yaml -e $(ROOT_DIR)/bin/$(PLUGIN_NAME).so
