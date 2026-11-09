package config

import (
	"crypto/tls"
	"crypto/x509"
	"fmt"

	"loop/pkg/storage"
)

const maxCertificateBytes = 4 << 20

func GetTLSConfig(cfg *Config) (*tls.Config, error) {
	if cfg == nil {
		return nil, fmt.Errorf("TLS configuration is unavailable")
	}
	next := &tls.Config{InsecureSkipVerify: !cfg.TLSVerify}
	if (cfg.CertFile == "") != (cfg.KeyFile == "") {
		return nil, fmt.Errorf("cert_file and key_file must be configured together")
	}
	if cfg.CertFile != "" {
		certificatePEM, err := storage.ReadFile(cfg.CertFile, maxCertificateBytes)
		if err != nil {
			return nil, fmt.Errorf("read client certificate: %w", err)
		}
		keyPEM, err := storage.ReadFile(cfg.KeyFile, maxCertificateBytes)
		if err != nil {
			return nil, fmt.Errorf("read client key: %w", err)
		}
		certificate, err := tls.X509KeyPair(certificatePEM, keyPEM)
		if err != nil {
			return nil, fmt.Errorf("load client certificate: %w", err)
		}
		next.Certificates = []tls.Certificate{certificate}
	}
	if cfg.CAFile != "" {
		data, err := storage.ReadFile(cfg.CAFile, maxCertificateBytes)
		if err != nil {
			return nil, fmt.Errorf("read CA certificate: %w", err)
		}
		roots, err := x509.SystemCertPool()
		if err != nil || roots == nil {
			roots = x509.NewCertPool()
		}
		if !roots.AppendCertsFromPEM(data) {
			return nil, fmt.Errorf("ca_file contains no valid certificates")
		}
		next.RootCAs = roots
	}
	return next, nil
}
