// Copyright 2026 nickytd
// SPDX-License-Identifier: Apache-2.0

package exporter

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestNewDynamicTLSConfig_empty(t *testing.T) {
	cfg, err := NewDynamicTLSConfig(TLSSettings{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg != nil {
		t.Fatalf("expected nil config, got %+v", cfg)
	}
}

func TestNewDynamicTLSConfig_insecureSkipVerify(t *testing.T) {
	cfg, err := NewDynamicTLSConfig(TLSSettings{InsecureSkipVerify: true})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg == nil {
		t.Fatal("expected non-nil config")
	}
	// InsecureSkipVerify: true with no CAFile — no VerifyConnection override,
	// so the field is set directly.
	if !cfg.InsecureSkipVerify {
		t.Error("InsecureSkipVerify should be true")
	}
}

func TestNewDynamicTLSConfig_badCAFile(t *testing.T) {
	_, err := NewDynamicTLSConfig(TLSSettings{CAFile: "/nonexistent/ca.pem"})
	if err == nil {
		t.Fatal("expected error for missing CA file, got nil")
	}
}

func TestNewDynamicTLSConfig_certWithoutKey(t *testing.T) {
	_, err := NewDynamicTLSConfig(TLSSettings{CertFile: "some.crt"})
	if err == nil {
		t.Fatal("expected error when only CertFile is set without KeyFile")
	}
}

func TestNewDynamicTLSConfig_keyWithoutCert(t *testing.T) {
	_, err := NewDynamicTLSConfig(TLSSettings{KeyFile: "some.key"})
	if err == nil {
		t.Fatal("expected error when only KeyFile is set without CertFile")
	}
}

func TestGetClientCertificateReloads(t *testing.T) {
	dir := t.TempDir()
	certFile := filepath.Join(dir, "client.crt")
	keyFile := filepath.Join(dir, "client.key")

	// Write first self-signed cert/key pair.
	writeSelfSignedCert(t, certFile, keyFile)

	cfg, err := NewDynamicTLSConfig(TLSSettings{CertFile: certFile, KeyFile: keyFile})
	if err != nil {
		t.Fatalf("NewDynamicTLSConfig: %v", err)
	}
	if cfg.GetClientCertificate == nil {
		t.Fatal("GetClientCertificate callback should be set")
	}

	got1, err := cfg.GetClientCertificate(nil)
	if err != nil {
		t.Fatalf("first GetClientCertificate: %v", err)
	}

	// Overwrite with a second distinct cert/key pair.
	writeSelfSignedCert(t, certFile, keyFile)

	got2, err := cfg.GetClientCertificate(nil)
	if err != nil {
		t.Fatalf("second GetClientCertificate: %v", err)
	}

	// The two leaf certificates must differ — rotation was picked up.
	if string(got1.Certificate[0]) == string(got2.Certificate[0]) {
		t.Error("GetClientCertificate returned the same cert after rotation; reload did not occur")
	}
}

// writeSelfSignedCert generates a fresh ECDSA self-signed cert/key and writes
// them as PEM to the given paths, overwriting any existing content.
func writeSelfSignedCert(t *testing.T, certFile, keyFile string) {
	t.Helper()

	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}

	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(time.Now().UnixNano()),
		Subject:      pkix.Name{CommonName: "test"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
	}
	certDER, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatalf("create certificate: %v", err)
	}

	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: certDER})
	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		t.Fatalf("marshal key: %v", err)
	}
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER})

	if err := os.WriteFile(certFile, certPEM, 0600); err != nil {
		t.Fatalf("write cert: %v", err)
	}
	if err := os.WriteFile(keyFile, keyPEM, 0600); err != nil {
		t.Fatalf("write key: %v", err)
	}
}
