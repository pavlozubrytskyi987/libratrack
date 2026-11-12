package tlsconf

import (
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"os"
	"path/filepath"
)

func ServerConfig(dir string) (*tls.Config, error) {
	cert, err := tls.LoadX509KeyPair(filepath.Join(dir, "server.pem"), filepath.Join(dir, "server-key.pem"))
	if err != nil {
		return nil, fmt.Errorf("load server cert: %w", err)
	}

	pool, err := loadPool(filepath.Join(dir, "ca.pem"))
	if err != nil {
		return nil, err
	}

	return &tls.Config{
		Certificates: []tls.Certificate{cert},
		ClientCAs:    pool,
		ClientAuth:   tls.RequireAndVerifyClientCert,
		MinVersion:   tls.VersionTLS12,
	}, nil
}

func ClientConfig(dir, certName string) (*tls.Config, error) {
	pool, err := loadPool(filepath.Join(dir, "ca.pem"))
	if err != nil {
		return nil, err
	}

	cfg := &tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS12}
	if certName != "none" {
		cert, err := tls.LoadX509KeyPair(
			filepath.Join(dir, certName+".pem"), filepath.Join(dir, certName+"-key.pem"))
		if err != nil {
			return nil, fmt.Errorf("load client cert %q: %w", certName, err)
		}
		cfg.GetClientCertificate = func(*tls.CertificateRequestInfo) (*tls.Certificate, error) {
			return &cert, nil
		}
	}

	return cfg, nil
}

func loadPool(caFile string) (*x509.CertPool, error) {
	pem, err := os.ReadFile(caFile)
	if err != nil {
		return nil, fmt.Errorf("read CA %s: %w", caFile, err)
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(pem) {
		return nil, fmt.Errorf("no certificates found in %s", caFile)
	}
	return pool, nil
}
