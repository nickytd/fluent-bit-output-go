//go:build e2e

package main

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("Fluent Bit output plugin", func() {
	var (
		soPath       string
		pluginDir    string
		collectorOut string
	)

	BeforeEach(func() {
		if _, err := exec.LookPath("fluent-bit"); err != nil {
			Skip("fluent-bit not found in PATH")
		}
		if _, err := exec.LookPath("otelcol"); err != nil {
			Skip("otelcol not found in PATH")
		}

		pluginDir = os.Getenv("PLUGIN_DIR")
		Expect(pluginDir).NotTo(BeEmpty(), "PLUGIN_DIR env variable must be set")

		soPath = filepath.Join(pluginDir, "go-out.so")
		Expect(soPath).To(BeAnExistingFile(), "plugin not built; run make build first")

		configPath := filepath.Join(pluginDir, "..", "fluent-bit.yaml")
		Expect(configPath).To(BeAnExistingFile(), "fluent-bit.yaml config not found at %s", configPath)

		collectorConfigPath := filepath.Join(pluginDir, "..", "otel-collector.yaml")
		Expect(collectorConfigPath).To(BeAnExistingFile(), "otel-collector.yaml not found at %s", collectorConfigPath)

		// Start OTel Collector
		collectorCtx, collectorCancel := context.WithCancel(context.Background())
		defer collectorCancel()

		collectorCmd := exec.CommandContext(collectorCtx, "otelcol", "--config", collectorConfigPath)
		var collectorStderr bytes.Buffer
		collectorCmd.Stderr = &collectorStderr

		err := collectorCmd.Start()
		Expect(err).NotTo(HaveOccurred(), "failed to start otelcol")

		// Wait for collector to be ready
		time.Sleep(2 * time.Second)

		// Run Fluent Bit with the plugin
		fbCtx, fbCancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer fbCancel()

		fbCmd := exec.CommandContext(fbCtx, "fluent-bit", "-e", soPath, "-c", configPath)
		var fbStderr bytes.Buffer
		fbCmd.Stderr = &fbStderr

		err = fbCmd.Run()
		if fbCtx.Err() == nil {
			Expect(err).NotTo(HaveOccurred(), "fluent-bit stderr: %s", fbStderr.String())
		}

		// Give collector time to process and flush debug output
		time.Sleep(1 * time.Second)

		// Stop collector
		collectorCancel()
		_ = collectorCmd.Wait()

		collectorOut = collectorStderr.String()
		Expect(collectorOut).NotTo(BeEmpty(), "collector produced no output")
	})

	It("delivers OTel envelope records with resource attributes to the collector", func() {
		Expect(collectorOut).To(ContainSubstring("component.name"))
		Expect(collectorOut).To(ContainSubstring("test.e2e"))
		Expect(collectorOut).To(ContainSubstring("e2e test log"))
	})

	It("delivers flat records with severity to the collector", func() {
		Expect(collectorOut).To(ContainSubstring("another log"))
		Expect(collectorOut).To(ContainSubstring("SeverityText: info"))
	})

	It("sets severity from the level field", func() {
		lines := strings.Split(collectorOut, "\n")
		foundSeverity := false
		for _, line := range lines {
			if strings.Contains(line, "SeverityNumber") && strings.Contains(line, "Info") {
				foundSeverity = true
				break
			}
		}
		Expect(foundSeverity).To(BeTrue(), "expected SeverityNumber Info in collector output:\n%s", collectorOut)
	})
})
