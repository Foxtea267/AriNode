package node

import (
	"fmt"
	"path/filepath"

	"github.com/Foxtea267/AriNode/api/panel"
	"github.com/Foxtea267/AriNode/common/cert"
	"github.com/Foxtea267/AriNode/conf"
	log "github.com/sirupsen/logrus"
)

// Resolve into a fresh Options value. Never mutate the configured machine
// template: discovery compares it every 30s, and sibling nodes may share it.
func (c *Controller) prepareNodeOptions(info *panel.NodeInfo) (*conf.Options, *cert.Material, string, error) {
	options := *c.Options
	if info.Security != panel.Tls {
		return &options, nil, "", nil
	}
	config := *conf.NewCertConfig()
	if c.CertConfig != nil {
		config = *c.CertConfig
	}
	options.CertConfig = &config
	root := options.CertStorePath
	if root == "" {
		root = filepath.Join("/etc/arinode", "certs")
	}
	store := cert.Store{Root: root}
	namespace := fmt.Sprintf("%s|machine=%d|%s|%d", c.apiClient.APIHost, c.apiClient.MachineID, info.Type, info.Id)
	source := "local"
	var material *cert.Material
	var err error
	if p := info.Common.CertConfig; p != nil && p.CertMode != "" && p.CertMode != "none" && p.CertMode != "auto" {
		source = "panel"
		switch p.CertMode {
		case "content":
			// A broken explicit certificate cannot silently fall back to an untrusted
			// one. Validation happens before removing the last working inbound.
			if p.CertContent == "" || p.KeyContent == "" {
				return nil, nil, "", fmt.Errorf("node %d: panel content certificate requires cert_content and key_content", info.Id)
			}
			material, err = store.Save(namespace, p.CertContent, p.KeyContent)
			if err != nil {
				return nil, nil, "", fmt.Errorf("node %d: panel content certificate: %w", info.Id, err)
			}
			config.CertMode, config.CertFile, config.KeyFile = "file", material.CertPath, material.KeyPath
		case "file":
			config.CertMode, config.CertFile, config.KeyFile = "file", p.CertFile, p.KeyFile
		case "self", "dns", "http":
			config.CertMode, config.CertDomain = p.CertMode, p.Domain
			if config.CertDomain == "" {
				config.CertDomain = info.TLSSettings.ServerName
			}
			config.Email, config.Provider, config.DNSEnv, config.HTTPPort = p.Email, p.DNSProvider, p.DNSEnv, p.HTTPPort
			config.CertFile, config.KeyFile = p.CertFile, p.KeyFile
			if (p.CertMode == "dns" || p.CertMode == "http") && config.CertFile == "" && config.KeyFile == "" {
				if p.HTTPPort < 0 || p.HTTPPort > 65535 {
					return nil, nil, "", fmt.Errorf("node %d: invalid certificate HTTP port", info.Id)
				}
				config.CertFile, config.KeyFile, err = store.ACMEPaths(namespace, p.CertMode, config.CertDomain)
				if err != nil {
					return nil, nil, "", err
				}
			}
			if config.CertMode == "self" && config.CertFile == "" && config.KeyFile == "" {
				material, err = store.SelfSigned(namespace, config.CertDomain)
				if err != nil {
					return nil, nil, "", err
				}
				config.CertMode, config.CertFile, config.KeyFile = "file", material.CertPath, material.KeyPath
			}
		default:
			return nil, nil, "", fmt.Errorf("node %d: unsupported panel cert_mode %q", info.Id, p.CertMode)
		}
	}
	if config.CertMode == "auto" {
		if !info.TLSSettings.AllowInsecure {
			return nil, nil, "", fmt.Errorf("node %d: no panel/local TLS certificate; automatic self-signed certificate requires panel allow_insecure", info.Id)
		}
		source = "self-signed"
		material, err = store.SelfSigned(namespace, info.TLSSettings.ServerName)
		if err != nil {
			return nil, nil, "", err
		}
		config.CertMode, config.CertFile, config.KeyFile = "file", material.CertPath, material.KeyPath
	}
	if config.CertMode == "self" && source == "local" {
		source = "self-signed"
	}
	if err := c.requestCert(&config); err != nil {
		return nil, nil, "", err
	}
	if config.CertMode == "none" || config.CertMode == "" {
		return &options, nil, "", nil
	}
	if material == nil {
		material, err = cert.Load(config.CertFile, config.KeyFile)
		if err != nil {
			return nil, nil, "", fmt.Errorf("node %d: %s TLS certificate: %w", info.Id, source, err)
		}
	}
	if name := info.TLSSettings.ServerName; name != "" {
		if err := material.Leaf.VerifyHostname(name); err != nil {
			log.WithFields(log.Fields{"node_id": info.Id, "server_name": name, "cert_source": source, "cert_fingerprint": material.Fingerprint}).Warn("TLS server_name is not covered by certificate SAN; verified clients using this name will fail")
		}
	}
	return &options, material, source, nil
}

func (c *Controller) logCertificate(info *panel.NodeInfo, material *cert.Material, source string) {
	if material == nil {
		return
	}
	log.WithFields(log.Fields{"node_id": info.Id, "node": c.tag, "cert_source": source, "cert_fingerprint": material.Fingerprint,
		"cert_path": material.CertPath, "key_path": material.KeyPath}).Info("TLS certificate active")
}
