// Package tlsutil provides the server's TLS certificate.
//
// By default the server uses a self-signed certificate created on first start.
// Apps trust it by pinning its SHA-256 fingerprint at pairing time (the same
// fingerprint is printed by `homebackupd pair` and advertised over Bonjour),
// so no certificate authority is needed on a home network. A certificate
// from elsewhere (e.g. `tailscale cert`) can be used instead.
package tlsutil

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
	"errors"
	"fmt"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Load reads a certificate and key from PEM files.
func Load(certFile, keyFile string) (tls.Certificate, error) {
	cert, err := tls.LoadX509KeyPair(certFile, keyFile)
	if err != nil {
		return tls.Certificate{}, err
	}
	cert.Leaf, err = x509.ParseCertificate(cert.Certificate[0])
	return cert, err
}

// LoadOrCreateSelfSigned loads the self-signed certificate in dir, creating
// it if needed. The certificate is valid for 10 years for the host name,
// <host>.local, localhost, and the given IPs.
func LoadOrCreateSelfSigned(dir string, now time.Time) (tls.Certificate, error) {
	certFile, keyFile := filepath.Join(dir, "cert.pem"), filepath.Join(dir, "key.pem")
	if cert, err := Load(certFile, keyFile); err == nil {
		return cert, nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return tls.Certificate{}, fmt.Errorf("load %s: %w", certFile, err)
	}

	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return tls.Certificate{}, err
	}
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		return tls.Certificate{}, err
	}
	host, _ := os.Hostname()
	host = strings.TrimSuffix(host, ".local")
	tmpl := &x509.Certificate{
		SerialNumber:          serial,
		Subject:               pkix.Name{CommonName: "homebackup " + host},
		NotBefore:             now.Add(-time.Hour),
		NotAfter:              now.AddDate(10, 0, 0),
		KeyUsage:              x509.KeyUsageDigitalSignature,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
		DNSNames:              []string{"localhost"},
		IPAddresses:           append([]net.IP{net.IPv4(127, 0, 0, 1), net.IPv6loopback}, localIPs()...),
	}
	if host != "" {
		tmpl.DNSNames = append(tmpl.DNSNames, host, host+".local")
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		return tls.Certificate{}, err
	}
	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		return tls.Certificate{}, err
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return tls.Certificate{}, err
	}
	if err := os.WriteFile(keyFile, pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER}), 0o600); err != nil {
		return tls.Certificate{}, err
	}
	if err := os.WriteFile(certFile, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0o644); err != nil {
		return tls.Certificate{}, err
	}
	return Load(certFile, keyFile)
}

// localIPs returns the machine's non-loopback unicast addresses.
func localIPs() []net.IP {
	addrs, err := net.InterfaceAddrs()
	if err != nil {
		return nil
	}
	var out []net.IP
	for _, a := range addrs {
		if ipn, ok := a.(*net.IPNet); ok && !ipn.IP.IsLoopback() && !ipn.IP.IsLinkLocalUnicast() {
			out = append(out, ipn.IP)
		}
	}
	return out
}

// Fingerprint returns the SHA-256 of the certificate's public key (SPKI), as
// lowercase hex. Pinning the key rather than the certificate keeps the pin
// valid if the certificate is renewed with the same key.
func Fingerprint(cert tls.Certificate) string {
	h := sha256.Sum256(cert.Leaf.RawSubjectPublicKeyInfo)
	return hex.EncodeToString(h[:])
}

// ShortFingerprint formats the first 8 bytes of a fingerprint for people to
// compare, e.g. "3F2A 91C0 7B4E 22D1".
func ShortFingerprint(fp string) string {
	if len(fp) < 16 {
		return strings.ToUpper(fp)
	}
	s := strings.ToUpper(fp[:16])
	return s[0:4] + " " + s[4:8] + " " + s[8:12] + " " + s[12:16]
}
