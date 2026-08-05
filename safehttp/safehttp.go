// Package safehttp provides an HTTP client hardened against SSRF.
//
// Herald fetches feed URLs supplied by anonymous users, so the fetcher must
// not be usable to reach internal or private network resources (cloud
// metadata endpoints, loopback services, the tailnet, RFC1918 hosts). The
// protection is enforced in the dialer's Control hook, which runs after DNS
// resolution against the concrete IP about to be dialed. That placement means
// it also covers HTTP redirects and DNS-rebinding, which URL-string checks
// alone miss.
package safehttp

import (
	"fmt"
	"net"
	"net/http"
	"net/url"
	"syscall"
	"time"
)

// MaxBodyBytes caps how much of a feed response is read. Feeds are text; this
// bounds memory even if a target streams unbounded data. Callers are expected
// to wrap the response body in an io.LimitReader with this limit.
const MaxBodyBytes = 10 << 20 // 10 MiB

// extraBlocked lists non-public ranges not already covered by the net.IP
// helpers used in isPublicIP.
var extraBlocked = func() []*net.IPNet {
	cidrs := []string{
		"100.64.0.0/10",   // RFC 6598 CGNAT (Tailscale uses this range)
		"192.0.0.0/24",    // RFC 6890 IETF protocol assignments
		"192.0.2.0/24",    // TEST-NET-1
		"198.18.0.0/15",   // RFC 2544 benchmarking
		"198.51.100.0/24", // TEST-NET-2
		"203.0.113.0/24",  // TEST-NET-3
		"64:ff9b::/96",    // NAT64
		"100::/64",        // RFC 6666 discard-only
	}
	nets := make([]*net.IPNet, 0, len(cidrs))
	for _, c := range cidrs {
		if _, n, err := net.ParseCIDR(c); err == nil {
			nets = append(nets, n)
		}
	}
	return nets
}()

// isPublicIP reports whether ip is a globally routable address that Herald is
// permitted to connect to.
func isPublicIP(ip net.IP) bool {
	if ip.IsLoopback() || // 127.0.0.0/8, ::1
		ip.IsUnspecified() || // 0.0.0.0, ::
		ip.IsLinkLocalUnicast() || // 169.254.0.0/16 (cloud metadata), fe80::/10
		ip.IsLinkLocalMulticast() ||
		ip.IsInterfaceLocalMulticast() ||
		ip.IsMulticast() ||
		ip.IsPrivate() { // RFC 1918, RFC 4193 fc00::/7
		return false
	}
	for _, blocked := range extraBlocked {
		if blocked.Contains(ip) {
			return false
		}
	}
	return true
}

// controlDial aborts a connection whose resolved address is not public. It is
// invoked for every dial, including those made while following redirects.
func controlDial(_, address string, _ syscall.RawConn) error {
	host, _, err := net.SplitHostPort(address)
	if err != nil {
		return fmt.Errorf("safehttp: cannot parse dial address %q: %w", address, err)
	}
	ip := net.ParseIP(host)
	if ip == nil {
		// The resolver hands us a literal IP; anything else is unexpected.
		return fmt.Errorf("safehttp: unexpected non-IP dial address %q", host)
	}
	if !isPublicIP(ip) {
		return fmt.Errorf("safehttp: refusing to connect to non-public address %s", ip)
	}
	return nil
}

// hardenedTransport is shared across clients so connections are pooled. The
// dialer's Control hook enforces the IP policy on every connection.
var hardenedTransport = &http.Transport{
	Proxy: http.ProxyFromEnvironment,
	DialContext: (&net.Dialer{
		Timeout:   10 * time.Second,
		KeepAlive: 30 * time.Second,
		Control:   controlDial,
	}).DialContext,
	ForceAttemptHTTP2:     true,
	MaxIdleConns:          100,
	IdleConnTimeout:       90 * time.Second,
	TLSHandshakeTimeout:   10 * time.Second,
	ExpectContinueTimeout: 1 * time.Second,
}

// Client returns an *http.Client that refuses to connect to non-public
// addresses. timeout bounds the whole request, including redirects.
func Client(timeout time.Duration) *http.Client {
	return &http.Client{
		Timeout:   timeout,
		Transport: hardenedTransport,
	}
}

// ValidateURL is a fast pre-check that rejects URLs Herald will never fetch:
// anything that is not http/https, or is missing a host. It does not perform
// DNS resolution or enforce the IP policy -- the dialer does that at connect
// time -- but it gives callers a clear, early error for obviously bad input.
func ValidateURL(raw string) error {
	u, err := url.Parse(raw)
	if err != nil {
		return fmt.Errorf("invalid URL: %w", err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return fmt.Errorf("unsupported URL scheme %q (only http and https are allowed)", u.Scheme)
	}
	if u.Hostname() == "" {
		return fmt.Errorf("URL has no host")
	}
	return nil
}
