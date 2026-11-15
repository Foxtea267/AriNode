package panel

import (
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"net"
	"net/url"

	"github.com/go-resty/resty/v2"
)

// Debug set the client debug for client
func (c *Client) Debug() {
	// Resty's debug output contains request URIs, query credentials and bodies.
	c.client.SetDebug(false)
}

func SafePanelAddress(raw string) string {
	u, err := url.Parse(raw)
	if err != nil || u.Hostname() == "" || (u.Scheme != "https" && u.Scheme != "http") || u.User != nil {
		return "<invalid-panel-url>"
	}
	// Preserve valid base paths (including a trailing slash) for legacy node
	// tags while excluding query credentials, fragments and URL userinfo.
	return u.Scheme + "://" + u.Host + u.EscapedPath()
}

func safeEndpoint(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return "<invalid-endpoint>"
	}
	if u.Path == "" {
		return "/"
	}
	return u.EscapedPath()
}

func panelErrorKind(err error) string {
	var verifyErr *tls.CertificateVerificationError
	if errors.As(err, &verifyErr) {
		var unknown x509.UnknownAuthorityError
		if errors.As(err, &unknown) {
			return "tls_unknown_authority"
		}
		var host x509.HostnameError
		if errors.As(err, &host) {
			return "tls_hostname"
		}
		var invalid x509.CertificateInvalidError
		if errors.As(err, &invalid) {
			if invalid.Reason == x509.Expired {
				return "tls_expired"
			}
			return "tls_certificate_invalid"
		}
		return "tls_certificate_verification"
	}
	var recordErr tls.RecordHeaderError
	if errors.As(err, &recordErr) {
		return "tls_record"
	}
	var opErr *net.OpError
	if errors.As(err, &opErr) {
		if opErr.Timeout() {
			return "network_timeout"
		}
		return "network"
	}
	return "request"
}

type quietRestyLogger struct{}

func (quietRestyLogger) Errorf(string, ...interface{}) {}
func (quietRestyLogger) Warnf(string, ...interface{})  {}
func (quietRestyLogger) Debugf(string, ...interface{}) {}

func (c *Client) checkResponse(res *resty.Response, path string, err error) error {
	if err != nil {
		return panelRequestError(path, err)
	}
	if res == nil {
		return fmt.Errorf("panel request %s failed: empty response", safeEndpoint(path))
	}
	if res.StatusCode() >= 400 {
		return fmt.Errorf("panel request %s failed: HTTP %d", safeEndpoint(path), res.StatusCode())
	}
	return nil
}

func panelRequestError(path string, err error) error {
	return fmt.Errorf("panel request %s failed (%s)", safeEndpoint(path), panelErrorKind(err))
}
