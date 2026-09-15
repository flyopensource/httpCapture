package main

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"encoding/pem"
	"fmt"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"strings"
	"time"
)

type controlIdentity struct {
	Certificate tls.Certificate
	DER         []byte
	SHA256      string
}

func loadOrCreateControlIdentity(host string) (controlIdentity, error) {
	directory, err := configDirectory()
	if err != nil {
		return controlIdentity{}, err
	}
	certPath := filepath.Join(directory, "control-cert.pem")
	keyPath := filepath.Join(directory, "control-key.pem")
	certificate, certErr := os.ReadFile(certPath)
	key, keyErr := os.ReadFile(keyPath)
	if certErr == nil && keyErr == nil {
		return parseControlIdentity(certificate, key)
	}
	if certErr != nil && !os.IsNotExist(certErr) {
		return controlIdentity{}, fmt.Errorf("读取控制服务证书: %w", certErr)
	}
	if keyErr != nil && !os.IsNotExist(keyErr) {
		return controlIdentity{}, fmt.Errorf("读取控制服务私钥: %w", keyErr)
	}
	return createControlIdentity(certPath, keyPath, host)
}

func createControlIdentity(certPath, keyPath, host string) (controlIdentity, error) {
	privateKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return controlIdentity{}, fmt.Errorf("生成控制服务私钥: %w", err)
	}
	serialLimit := new(big.Int).Lsh(big.NewInt(1), 128)
	serial, err := rand.Int(rand.Reader, serialLimit)
	if err != nil {
		return controlIdentity{}, err
	}
	template := x509.Certificate{
		SerialNumber: serial,
		Subject: pkix.Name{
			CommonName: "httpcapture control",
		},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().AddDate(10, 0, 0),
		KeyUsage:              x509.KeyUsageDigitalSignature,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
	}
	if ip := net.ParseIP(host); ip != nil {
		template.IPAddresses = []net.IP{ip}
	} else if strings.TrimSpace(host) != "" {
		template.DNSNames = []string{strings.TrimSpace(host)}
	}
	der, err := x509.CreateCertificate(rand.Reader, &template, &template, &privateKey.PublicKey, privateKey)
	if err != nil {
		return controlIdentity{}, fmt.Errorf("生成控制服务证书: %w", err)
	}
	keyDER, err := x509.MarshalECPrivateKey(privateKey)
	if err != nil {
		return controlIdentity{}, err
	}
	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER})
	if err := secureWrite(certPath, certPEM); err != nil {
		return controlIdentity{}, err
	}
	if err := secureWrite(keyPath, keyPEM); err != nil {
		return controlIdentity{}, err
	}
	return parseControlIdentity(certPEM, keyPEM)
}

func parseControlIdentity(certPEM, keyPEM []byte) (controlIdentity, error) {
	certificate, err := tls.X509KeyPair(certPEM, keyPEM)
	if err != nil {
		return controlIdentity{}, fmt.Errorf("解析控制服务 TLS 身份: %w", err)
	}
	if len(certificate.Certificate) == 0 {
		return controlIdentity{}, fmt.Errorf("控制服务证书为空")
	}
	digest := sha256.Sum256(certificate.Certificate[0])
	return controlIdentity{
		Certificate: certificate,
		DER:         certificate.Certificate[0],
		SHA256:      strings.ToUpper(hex.EncodeToString(digest[:])),
	}, nil
}
