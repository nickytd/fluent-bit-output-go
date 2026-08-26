// Copyright 2026 nickytd
// SPDX-License-Identifier: Apache-2.0

//go:build e2e

package main

import (
	"testing"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

func TestE2E(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "goout suite")
}
