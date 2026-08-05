package web

import (
	"net/http"
	"testing"
)

func TestClientIP(t *testing.T) {
	tests := []struct {
		name       string
		remoteAddr string
		xff        string
		want       string
	}{
		{
			name:       "direct connection ignores forwarded header",
			remoteAddr: "203.0.113.5:44321",
			xff:        "1.2.3.4",
			want:       "203.0.113.5",
		},
		{
			name:       "proxied request trusts forwarded header",
			remoteAddr: "127.0.0.1:8085",
			xff:        "198.51.100.7",
			want:       "198.51.100.7",
		},
		{
			name:       "proxied request uses rightmost forwarded entry",
			remoteAddr: "127.0.0.1:8085",
			xff:        "1.2.3.4, 198.51.100.7",
			want:       "198.51.100.7",
		},
		{
			name:       "spoofed non-ip entry is skipped",
			remoteAddr: "127.0.0.1:8085",
			xff:        "198.51.100.7, not-an-ip",
			want:       "198.51.100.7",
		},
		{
			name:       "proxied request without header falls back to peer",
			remoteAddr: "127.0.0.1:8085",
			xff:        "",
			want:       "127.0.0.1",
		},
		{
			name:       "ipv6 loopback peer is trusted",
			remoteAddr: "[::1]:8085",
			xff:        "198.51.100.7",
			want:       "198.51.100.7",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := &http.Request{
				RemoteAddr: tt.remoteAddr,
				Header:     http.Header{},
			}
			if tt.xff != "" {
				r.Header.Set("X-Forwarded-For", tt.xff)
			}
			if got := clientIP(r); got != tt.want {
				t.Errorf("clientIP() = %q, want %q", got, tt.want)
			}
		})
	}
}
