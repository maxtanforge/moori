package main

import (
	"crypto/x509"
	"errors"
	"os"
)

func controlRoots(path string) (*x509.CertPool, error) {
	if path == "" {
		return nil, nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	roots := x509.NewCertPool()
	if !roots.AppendCertsFromPEM(data) {
		return nil, errors.New("control CA contains no certificates")
	}
	return roots, nil
}
