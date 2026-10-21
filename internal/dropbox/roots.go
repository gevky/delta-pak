package dropbox

import (
	"crypto/tls"
	"crypto/x509"
	_ "embed"
	"net"
	"net/http"
	"sync"
	"time"
)

// caBundle is Mozilla's CA root store, as distributed by the curl project.
//
// These binaries are built with CGO_ENABLED=0 so one file runs on every
// device. That has a consequence which is easy to miss until TLS fails in the
// field: without cgo, Go cannot read macOS's keychain, and a handheld running
// a minimal Linux may ship no certificate store at all. In both cases
// x509.SystemCertPool() yields nothing and every HTTPS call dies with
// "certificate signed by unknown authority" -- not a proxy problem, and not
// something the user can fix. Carrying the roots in the binary makes TLS
// verification work identically everywhere, and it stays strict: this only
// supplies roots, it never skips verification.
//
//go:embed certs/cacert.pem
var caBundle []byte

var (
	rootsOnce sync.Once
	rootPool  *x509.CertPool
)

// rootCAs returns the system roots with the embedded bundle added, or just the
// embedded bundle when the system has no usable store.
func rootCAs() *x509.CertPool {
	rootsOnce.Do(func() {
		pool, err := x509.SystemCertPool()
		if err != nil || pool == nil {
			pool = x509.NewCertPool()
		}
		pool.AppendCertsFromPEM(caBundle)
		rootPool = pool
	})
	return rootPool
}

// newHTTPClient builds the client every Dropbox call uses.
func newHTTPClient(timeout time.Duration) *http.Client {
	return &http.Client{
		Timeout: timeout,
		Transport: &http.Transport{
			TLSClientConfig: &tls.Config{
				RootCAs:    rootCAs(),
				MinVersion: tls.VersionTLS12,
			},
			DialContext: (&net.Dialer{
				// A handheld can take a moment to bring Wi-Fi up after boot.
				Timeout:   30 * time.Second,
				KeepAlive: 30 * time.Second,
			}).DialContext,
			TLSHandshakeTimeout:   20 * time.Second,
			ResponseHeaderTimeout: 60 * time.Second,
			Proxy:                 http.ProxyFromEnvironment,
		},
	}
}
