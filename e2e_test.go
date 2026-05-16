//go:build e2e

package main

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"go.opentelemetry.io/collector/pdata/plog"
)

var _ = Describe("Fluent Bit output plugin", func() {
	var (
		soPath    string
		pluginDir string
		logs      plog.Logs
	)

	BeforeEach(func() {
		if _, err := exec.LookPath("fluent-bit"); err != nil {
			Skip("fluent-bit not found in PATH")
		}

		pluginDir = os.Getenv("PLUGIN_DIR")
		Expect(pluginDir).NotTo(BeEmpty(), "PLUGIN_DIR env variable must be set")

		soPath = filepath.Join(pluginDir, "go-out.so")
		Expect(soPath).To(BeAnExistingFile(), "plugin not built; run make build first")

		configPath := filepath.Join(pluginDir, "..", "fluent-bit.yaml")
		Expect(configPath).To(BeAnExistingFile(), "fluent-bit.yaml config not found at %s", configPath)

		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()

		cmd := exec.CommandContext(ctx, "fluent-bit", "-e", soPath, "-c", configPath)
		var stdout, stderr bytes.Buffer
		cmd.Stdout = &stdout
		cmd.Stderr = &stderr

		err := cmd.Run()
		if ctx.Err() == nil {
			Expect(err).NotTo(HaveOccurred(), "stderr: %s\nstdout: %s", stderr.String(), stdout.String())
		}

		Expect(stdout.String()).NotTo(BeEmpty(), "no output; stderr: %s", stderr.String())

		logs = plog.NewLogs()
		unmarshaler := &plog.JSONUnmarshaler{}
		for _, line := range bytes.Split(stdout.Bytes(), []byte("\n")) {
			if len(bytes.TrimSpace(line)) == 0 {
				continue
			}
			parsed, err := unmarshaler.UnmarshalLogs(line)
			Expect(err).NotTo(HaveOccurred(), "raw line: %s", string(line))
			parsed.ResourceLogs().MoveAndAppendTo(logs.ResourceLogs())
		}
		Expect(logs.ResourceLogs().Len()).To(BeNumerically(">=", 2))
	})

	It("processes OTel envelope records with resource attributes", func() {
		var otelRL plog.ResourceLogs
		found := false
		for i := range logs.ResourceLogs().Len() {
			rl := logs.ResourceLogs().At(i)
			if v, ok := rl.Resource().Attributes().Get("component.name"); ok {
				Expect(v.Str()).To(Equal("test.e2e"))
				otelRL = rl
				found = true
				break
			}
		}
		Expect(found).To(BeTrue(), "no ResourceLogs with 'component.name' resource attribute")

		Expect(otelRL.ScopeLogs().Len()).To(BeNumerically(">=", 1))
		sl := otelRL.ScopeLogs().At(0)
		Expect(sl.LogRecords().Len()).To(BeNumerically(">=", 1))

		lr := sl.LogRecords().At(0)
		msg, ok := lr.Attributes().Get("message")
		Expect(ok).To(BeTrue(), "expected 'message' attribute on OTel log record")
		Expect(msg.Str()).To(Equal("e2e test log"))
	})

	It("processes simple flat records", func() {
		var simpleRL plog.ResourceLogs
		found := false
		for i := range logs.ResourceLogs().Len() {
			rl := logs.ResourceLogs().At(i)
			if rl.Resource().Attributes().Len() == 0 {
				simpleRL = rl
				found = true
				break
			}
		}
		Expect(found).To(BeTrue(), "no ResourceLogs without resource attributes (simple record)")

		Expect(simpleRL.ScopeLogs().Len()).To(BeNumerically(">=", 1))
		sl := simpleRL.ScopeLogs().At(0)
		Expect(sl.LogRecords().Len()).To(BeNumerically(">=", 1))

		lr := sl.LogRecords().At(0)
		msg, ok := lr.Attributes().Get("message")
		Expect(ok).To(BeTrue(), "expected 'message' attribute on simple log record")
		Expect(msg.Str()).To(Equal("another log"))
	})
})
