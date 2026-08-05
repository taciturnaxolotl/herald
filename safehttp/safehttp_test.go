package safehttp

import (
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestIsPublicIP(t *testing.T) {
	blocked := []string{
		"127.0.0.1",       // loopback
		"::1",             // loopback v6
		"0.0.0.0",         // unspecified
		"169.254.169.254", // cloud metadata (link-local)
		"10.0.0.5",        // RFC1918
		"172.16.9.9",      // RFC1918
		"192.168.1.1",     // RFC1918
		"100.64.1.1",      // CGNAT / tailscale
		"fd00::1",         // ULA
		"fe80::1",         // link-local v6
		"198.18.0.1",      // benchmarking
	}
	for _, s := range blocked {
		if isPublicIP(net.ParseIP(s)) {
			t.Errorf("%s should be blocked but was treated as public", s)
		}
	}

	public := []string{
		"1.1.1.1",
		"8.8.8.8",
		"93.184.216.34", // example.com
		"2606:4700:4700::1111",
	}
	for _, s := range public {
		if !isPublicIP(net.ParseIP(s)) {
			t.Errorf("%s should be public but was blocked", s)
		}
	}
}

func TestValidateURL(t *testing.T) {
	bad := []string{
		"not-a-url",
		"://missing-scheme.com",
		"http://",
		"file:///etc/passwd",
		"ftp://example.com/x",
		"gopher://example.com:70/",
	}
	for _, u := range bad {
		if err := ValidateURL(u); err == nil {
			t.Errorf("ValidateURL(%q) should have failed", u)
		}
	}

	good := []string{
		"http://example.com/rss",
		"https://sub.example.com:8443/atom.xml",
	}
	for _, u := range good {
		if err := ValidateURL(u); err != nil {
			t.Errorf("ValidateURL(%q) unexpected error: %v", u, err)
		}
	}
}

// TestClientBlocksLoopback verifies the dialer actually refuses an internal
// address at connect time, even for a syntactically valid http URL.
func TestClientBlocksLoopback(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	// srv.URL is http://127.0.0.1:<port> -- a loopback address.
	_, err := Client(5 * time.Second).Get(srv.URL)
	if err == nil {
		t.Fatal("expected connection to loopback server to be refused")
	}
	if !strings.Contains(err.Error(), "non-public") {
		t.Errorf("expected a non-public address error, got: %v", err)
	}
}

// TestClientAllowsPublicDial confirms the Control hook permits a public IP.
// It dials a listener bound to a routable-looking address check via the hook
// directly, avoiding real network egress.
func TestControlAllowsPublic(t *testing.T) {
	// Simulate the dialer Control call for a public address.
	if err := controlDial("tcp", "8.8.8.8:443", nil); err != nil {
		t.Errorf("controlDial rejected a public address: %v", err)
	}
	if err := controlDial("tcp", "127.0.0.1:80", nil); err == nil {
		t.Error("controlDial allowed loopback")
	}
}
