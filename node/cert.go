package node

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"fmt"
	"math/big"
	"os"
	"time"

	"github.com/Foxtea267/AriNode/common/file"
	"github.com/Foxtea267/AriNode/conf"
	log "github.com/sirupsen/logrus"
)

func (c *Controller) renewCertTask() error {
	options := c.runtimeOptions.Load()
	if options == nil || options.CertConfig == nil {
		return nil
	}
	config := options.CertConfig
	if config.CertMode != "dns" && config.CertMode != "http" {
		return nil
	}
	l, err := NewLego(config)
	if err != nil {
		log.WithField("tag", c.tag).Info("new lego error: ", err)
		return nil
	}
	err = l.RenewCert()
	if err != nil {
		log.WithField("tag", c.tag).Info("renew cert error: ", err)
		return nil
	}
	return nil
}

func (c *Controller) requestCert(config *conf.CertConfig) error {
	switch config.CertMode {
	case "none", "", "auto":
	case "file":
		if config.CertFile == "" || config.KeyFile == "" {
			return fmt.Errorf("cert file path or key file path not exist")
		}
	case "dns", "http":
		if config.CertFile == "" || config.KeyFile == "" {
			return fmt.Errorf("cert file path or key file path not exist")
		}
		if file.IsExist(config.CertFile) && file.IsExist(config.KeyFile) {
			return nil
		}
		l, err := NewLego(config)
		if err != nil {
			return fmt.Errorf("create lego object error: %s", err)
		}
		err = l.CreateCert()
		if err != nil {
			return fmt.Errorf("create lego cert error: %s", err)
		}
	case "self":
		if config.CertFile == "" || config.KeyFile == "" {
			return fmt.Errorf("cert file path or key file path not exist")
		}
		if file.IsExist(config.CertFile) && file.IsExist(config.KeyFile) {
			return nil
		}
		err := generateSelfSslCertificate(
			config.CertDomain,
			config.CertFile,
			config.KeyFile)
		if err != nil {
			return fmt.Errorf("generate self cert error: %s", err)
		}
	default:
		return fmt.Errorf("unsupported certmode: %s", config.CertMode)
	}
	return nil
}

func generateSelfSslCertificate(domain, certPath, keyPath string) error {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		return err
	}
	tmpl := &x509.Certificate{
		Version:      3,
		SerialNumber: big.NewInt(time.Now().Unix()),
		Subject: pkix.Name{
			CommonName: domain,
		},
		DNSNames:              []string{domain},
		BasicConstraintsValid: true,
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		NotBefore:             time.Now(),
		NotAfter:              time.Now().AddDate(30, 0, 0),
	}
	cert, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, key.Public(), key)
	if err != nil {
		return err
	}
	f, err := os.OpenFile(certPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0644)
	if err != nil {
		return err
	}
	err = pem.Encode(f, &pem.Block{
		Type:  "CERTIFICATE",
		Bytes: cert,
	})
	closeErr := f.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	f, err = os.OpenFile(keyPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0600)
	if err != nil {
		return err
	}
	if err = f.Chmod(0600); err != nil {
		f.Close()
		return err
	}
	err = pem.Encode(f, &pem.Block{
		Type:  "RSA PRIVATE KEY",
		Bytes: x509.MarshalPKCS1PrivateKey(key),
	})
	closeErr = f.Close()
	if err != nil {
		return err
	}
	return closeErr
}
