package cert

import (
	"bytes"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

type Material struct {
	CertPath    string
	KeyPath     string
	Fingerprint string
	Leaf        *x509.Certificate
}

// Certificate and key are published as an immutable directory, not two separate
// replacements. Old generations remain usable by a failed reload's rollback.
type Store struct{ Root string }

// Serializes store publication and the automatic self-signed reference update.
// No network or ACME operation is performed while this lock is held.
var storeMu sync.Mutex

func digest(value []byte) string {
	sum := sha256.Sum256(value)
	return hex.EncodeToString(sum[:])
}

func Parse(certificate, key []byte) (*x509.Certificate, error) {
	if len(certificate) > 2<<20 || len(key) > 128<<10 {
		return nil, fmt.Errorf("certificate material exceeds size limit")
	}
	pair, err := tls.X509KeyPair(certificate, key)
	if err != nil {
		return nil, fmt.Errorf("invalid certificate chain or private key: %w", err)
	}
	for _, der := range pair.Certificate {
		if _, err := x509.ParseCertificate(der); err != nil {
			return nil, fmt.Errorf("invalid certificate chain: %w", err)
		}
	}
	leaf, err := x509.ParseCertificate(pair.Certificate[0])
	if err != nil {
		return nil, fmt.Errorf("invalid leaf certificate: %w", err)
	}
	return leaf, nil
}

func Load(certPath, keyPath string) (*Material, error) {
	certificate, err := os.ReadFile(certPath)
	if err != nil {
		return nil, fmt.Errorf("read certificate: %w", err)
	}
	key, err := os.ReadFile(keyPath)
	if err != nil {
		return nil, fmt.Errorf("read private key: %w", err)
	}
	leaf, err := Parse(certificate, key)
	if err != nil {
		return nil, err
	}
	return &Material{CertPath: certPath, KeyPath: keyPath, Fingerprint: digest(leaf.Raw), Leaf: leaf}, nil
}

func (s Store) directory(namespace string) string {
	return filepath.Join(s.Root, digest([]byte(namespace)))
}

// ACME renews these files itself; keep them separate from immutable panel content.
func (s Store) ACMEPaths(namespace, mode, domain string) (string, string, error) {
	directory := filepath.Join(s.directory(namespace), "acme-"+digest([]byte(mode+"\x00"+domain)))
	if err := os.MkdirAll(directory, 0700); err != nil {
		return "", "", err
	}
	return filepath.Join(directory, "cert.pem"), filepath.Join(directory, "key.pem"), nil
}

func (s Store) Save(namespace, certificate, key string) (*Material, error) {
	storeMu.Lock()
	defer storeMu.Unlock()
	certificate, key, err := CanonicalPair(certificate, key)
	if err != nil {
		return nil, err
	}
	return s.save(namespace, certificate, key)
}

func (s Store) save(namespace, certificate, key string) (*Material, error) {
	leaf, err := Parse([]byte(certificate), []byte(key))
	if err != nil {
		return nil, err
	}
	if now := time.Now(); now.Before(leaf.NotBefore) || !now.Before(leaf.NotAfter) {
		return nil, fmt.Errorf("panel certificate is outside its validity period")
	}
	parent := s.directory(namespace)
	generation := digest([]byte(certificate + "\x00" + key))
	target := filepath.Join(parent, generation)
	material := &Material{CertPath: filepath.Join(target, "cert.pem"), KeyPath: filepath.Join(target, "key.pem"), Fingerprint: digest(leaf.Raw), Leaf: leaf}
	if stat, err := os.Stat(target); err == nil {
		if !stat.IsDir() {
			return nil, fmt.Errorf("certificate generation path is not a directory")
		}
		for path, expected := range map[string]string{material.CertPath: certificate, material.KeyPath: key} {
			got, err := os.ReadFile(path)
			if err != nil || !bytes.Equal(got, []byte(expected)) {
				return nil, fmt.Errorf("cached certificate generation is incomplete or corrupted")
			}
			info, err := os.Stat(path)
			if err != nil {
				return nil, err
			}
			if info.Mode().Perm() != 0600 {
				if err := os.Chmod(path, 0600); err != nil {
					return nil, err
				}
			}
		}
		return material, nil // In particular, leave file contents and mtimes unchanged.
	} else if !os.IsNotExist(err) {
		return nil, err
	}
	if err := os.MkdirAll(parent, 0700); err != nil {
		return nil, fmt.Errorf("create certificate store: %w", err)
	}
	temp, err := os.MkdirTemp(parent, ".pending-")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(temp)
	for name, data := range map[string]string{"cert.pem": certificate, "key.pem": key} {
		if err := writePrivate(filepath.Join(temp, name), []byte(data)); err != nil {
			return nil, err
		}
	}
	if err := os.Rename(temp, target); err != nil {
		return nil, fmt.Errorf("publish certificate generation: %w", err)
	}
	return material, nil
}

func writePrivate(path string, data []byte) error {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return err
	}
	_, err = f.Write(data)
	if err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err != nil {
		return err
	}
	return closeErr
}

func (s Store) SelfSigned(namespace, name string) (*Material, error) {
	storeMu.Lock()
	defer storeMu.Unlock()
	if name == "" {
		name = "arinode.local"
	}
	ref := filepath.Join(s.directory(namespace), "self-"+digest([]byte(name)))
	if value, err := os.ReadFile(ref); err == nil {
		generation := strings.TrimSpace(string(value))
		if raw, err := hex.DecodeString(generation); err == nil && len(raw) == sha256.Size {
			base := filepath.Join(s.directory(namespace), generation)
			if material, err := Load(filepath.Join(base, "cert.pem"), filepath.Join(base, "key.pem")); err == nil && material.Leaf.VerifyHostname(name) == nil && !time.Now().Before(material.Leaf.NotBefore) && time.Now().Before(material.Leaf.NotAfter) {
				return material, nil
			}
		}
	} else if !os.IsNotExist(err) {
		return nil, err
	}
	certificate, key, err := SelfSigned(name)
	if err != nil {
		return nil, err
	}
	material, err := s.save(namespace, strings.Join(certificate, "\n"), strings.Join(key, "\n"))
	if err != nil {
		return nil, err
	}
	temp, err := os.CreateTemp(filepath.Dir(ref), ".self-")
	if err != nil {
		return nil, err
	}
	tempName := temp.Name()
	defer os.Remove(tempName)
	if _, err = temp.WriteString(filepath.Base(filepath.Dir(material.CertPath))); err == nil {
		err = temp.Sync()
	}
	closeErr := temp.Close()
	if err != nil {
		return nil, err
	}
	if closeErr != nil {
		return nil, closeErr
	}
	if err := os.Rename(tempName, ref); err != nil {
		return nil, err
	}
	return material, nil
}
