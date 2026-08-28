// Copyright 2026 nickytd
// SPDX-License-Identifier: Apache-2.0

package exporter

import (
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"os"
)

// TLSSettings holds file paths for TLS configuration. All fields are optional;
// a zero-value TLSSettings produces a nil *tls.Config (system defaults).
type TLSSettings struct {
	CAFile             string
	CertFile           string
	KeyFile            string
	InsecureSkipVerify bool
}

// NewDynamicTLSConfig returns a *tls.Config whose callbacks re-read certificate
// and CA files from disk on every new TLS handshake, enabling zero-restart
// certificate rotation. Returns nil when all settings are zero (system defaults).
//
// GetClientCertificate is set when both CertFile and KeyFile are provided — the
// pair is loaded fresh on each handshake so rotated client certs are picked up
// automatically.
//
// VerifyConnection is set when CAFile is provided — the CA pool is rebuilt from
// disk on each handshake so rotated CA certificates are picked up without a
// restart. Standard hostname verification still applies unless InsecureSkipVerify
// is set.
func NewDynamicTLSConfig(s TLSSettings) (*tls.Config, error) {
	if s.CAFile == "" && s.CertFile == "" && s.KeyFile == "" && !s.InsecureSkipVerify {
		return nil, nil
	}

	// Validate at construction time so config errors surface at plugin init,
	// not silently on the first handshake.
	if s.CAFile != "" {
		if _, err := loadCACertPool(s.CAFile); err != nil {
			return nil, err
		}
	}
	if s.CertFile != "" || s.KeyFile != "" {
		if s.CertFile == "" || s.KeyFile == "" {
			return nil, fmt.Errorf("tls_cert_file and tls_key_file must both be set for mTLS")
		}
		if _, err := tls.LoadX509KeyPair(s.CertFile, s.KeyFile); err != nil {
			return nil, fmt.Errorf("load client cert/key: %w", err)
		}
	}

	cfg := &tls.Config{
		InsecureSkipVerify: s.InsecureSkipVerify, // #nosec G402 -- user-controlled opt-in via plugin config
	}

	if s.CertFile != "" {
		certFile, keyFile := s.CertFile, s.KeyFile
		cfg.GetClientCertificate = func(*tls.CertificateRequestInfo) (*tls.Certificate, error) {
			cert, err := tls.LoadX509KeyPair(certFile, keyFile)
			if err != nil {
				return nil, fmt.Errorf("reload client cert/key: %w", err)
			}
			return &cert, nil
		}
	}

	if s.CAFile != "" {
		caFile := s.CAFile
		cfg.VerifyConnection = func(cs tls.ConnectionState) error {
			pool, err := loadCACertPool(caFile)
			if err != nil {
				return err
			}
			opts := x509.VerifyOptions{
				Roots:         pool,
				DNSName:       cs.ServerName,
				Intermediates: x509.NewCertPool(),
			}
			for _, cert := range cs.PeerCertificates[1:] {
				opts.Intermediates.AddCert(cert)
			}
			_, err = cs.PeerCertificates[0].Verify(opts)
			return err
		}
		// Disable the default verification so our VerifyConnection callback is
		// the sole verifier (avoids double verification with stale system roots).
		cfg.InsecureSkipVerify = true // #nosec G402 -- standard verification is replaced by VerifyConnection callback
	}

	return cfg, nil
}

func loadCACertPool(caFile string) (*x509.CertPool, error) {
	pem, err := os.ReadFile(caFile) // #nosec G304 -- path comes from plugin config supplied by the operator, not user input
	if err != nil {
		return nil, fmt.Errorf("read CA file %q: %w", caFile, err)
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(pem) {
		return nil, fmt.Errorf("no valid certificates found in CA file %q", caFile)
	}
	return pool, nil
}
