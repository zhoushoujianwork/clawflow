package coordinator

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
	"math/big"
	"net"
	"os"
	"path/filepath"
	"time"
)

// LoadCertificate atomically publishes one private PEM bundle. Concurrent first
// starts load the winning bundle, so a previously advertised pin never changes.
func LoadCertificate(dir string) (tls.Certificate, string, error) {
	var empty tls.Certificate
	if err := os.MkdirAll(dir, 0700); err != nil {
		return empty, "", err
	}
	path := filepath.Join(dir, "coordinator.pem")
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		key, e := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
		if e != nil {
			return empty, "", e
		}
		serial, e := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
		if e != nil {
			return empty, "", e
		}
		now := time.Now()
		template := &x509.Certificate{SerialNumber: serial, Subject: pkix.Name{CommonName: "ClawFlow coordinator"}, NotBefore: now.Add(-time.Hour), NotAfter: now.AddDate(10, 0, 0), KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}, DNSNames: []string{"localhost"}, IPAddresses: []net.IP{net.ParseIP("127.0.0.1"), net.ParseIP("::1")}}
		der, e := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
		if e != nil {
			return empty, "", e
		}
		private, e := x509.MarshalECPrivateKey(key)
		if e != nil {
			return empty, "", e
		}
		bundle := append(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: private})...)
		f, e := os.CreateTemp(dir, ".certificate-*")
		if e != nil {
			return empty, "", e
		}
		defer os.Remove(f.Name())
		_, e = f.Write(bundle)
		if e == nil {
			e = f.Sync()
		}
		closeErr := f.Close()
		if e != nil {
			return empty, "", e
		}
		if closeErr != nil {
			return empty, "", closeErr
		}
		if e = os.Link(f.Name(), path); e != nil && !errors.Is(e, os.ErrExist) {
			return empty, "", e
		}
		data, err = os.ReadFile(path)
	}
	if err != nil {
		return empty, "", err
	}
	if err = os.Chmod(path, 0600); err != nil {
		return empty, "", err
	}
	cert, err := tls.X509KeyPair(data, data)
	if err != nil {
		return empty, "", err
	}
	fingerprint := sha256.Sum256(cert.Certificate[0])
	return cert, hex.EncodeToString(fingerprint[:]), nil
}
