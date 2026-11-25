package main

import (
	"crypto/tls"
	"errors"
)

func loadTransportSecurity(cert, key string) (*tls.Config, error) {
	if (cert == "") != (key == "") {
		return nil, errors.New("tls-cert and tls-key must be configured together")
	}
	if cert == "" {
		return nil, nil
	}
	pair, err := tls.LoadX509KeyPair(cert, key)
	if err != nil {
		return nil, err
	}
	return &tls.Config{MinVersion: tls.VersionTLS12, Certificates: []tls.Certificate{pair}}, nil
}
