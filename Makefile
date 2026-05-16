PLUGIN_NAME          := go-out
ROOT_DIR             := $(shell dirname $(realpath $(lastword $(MAKEFILE_LIST))))

.PHONY: all build test unit-test e2e-test lint clean run

all: lint unit-test build

$(ROOT_DIR)/bin/$(PLUGIN_NAME).so: $(wildcard *.go) go.mod
	@mkdir -p $(ROOT_DIR)/bin
	go build -buildmode=c-shared -o $@ .

build: $(ROOT_DIR)/bin/$(PLUGIN_NAME).so

test: unit-test e2e-test

unit-test:
	@printf "Running unit tests...\n"
	@go test ./...

e2e-test: build
	@printf "Running end-to-end tests...\n"
	@PLUGIN_DIR=$(ROOT_DIR)/bin go test -tags=e2e ./...

lint:
	@printf "Running linters...\n"
	@go fix ./...
	@golangci-lint run

clean:
	@printf "Cleaning up build artifacts...\n"
	@rm -rf $(ROOT_DIR)/bin

run: $(ROOT_DIR)/bin/$(PLUGIN_NAME).so
	@fluent-bit -c $(ROOT_DIR)/fluent-bit.yaml -e $(ROOT_DIR)/bin/$(PLUGIN_NAME).so
