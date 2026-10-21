package dropbox

import (
	"crypto/tls"
	"crypto/x509"
	"net/http"
	"testing"
	"time"
)

// These binaries are built with CGO_ENABLED=0, which leaves Go unable to read
// macOS's keychain, and a minimal handheld Linux may carry no certificate
// store at all. Both cases produced "certificate signed by unknown authority"
// against Dropbox. This proves the embedded bundle alone verifies Dropbox, by
// trusting nothing else.
func TestEmbeddedRootsVerifyDropbox(t *testing.T) {
	if testing.Short() {
		t.Skip("network test")
	}
	onlyEmbedded := x509.NewCertPool()
	if !onlyEmbedded.AppendCertsFromPEM(caBundle) {
		t.Fatal("embedded CA bundle did not parse")
	}
	client := &http.Client{
		Timeout: 30 * time.Second,
		Transport: &http.Transport{
			TLSClientConfig: &tls.Config{RootCAs: onlyEmbedded, MinVersion: tls.VersionTLS12},
			Proxy:           http.ProxyFromEnvironment,
		},
	}
	for _, url := range []string{
		"https://api.dropboxapi.com/",
		"https://content.dropboxapi.com/",
		"https://www.dropbox.com/",
	} {
		resp, err := client.Get(url)
		if err != nil {
			t.Fatalf("%s: TLS verification failed with the embedded roots: %v", url, err)
		}
		resp.Body.Close()
	}
}

func TestRootCAsIncludesEmbeddedBundle(t *testing.T) {
	if len(caBundle) < 100_000 {
		t.Fatalf("embedded CA bundle looks truncated: %d bytes", len(caBundle))
	}
	if rootCAs() == nil {
		t.Fatal("rootCAs() returned nil")
	}
}
