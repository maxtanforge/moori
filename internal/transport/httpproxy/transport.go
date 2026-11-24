// Package httpproxy identifies protected HTTP requests at a trusted gateway.
package httpproxy

import (
	"net"
	"net/http"
	"net/netip"
)

// Peer returns the address of the socket peer. It ignores client-supplied headers.
func Peer(r *http.Request) netip.Addr {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return netip.Addr{}
	}
	addr, err := netip.ParseAddr(host)
	if err != nil {
		return netip.Addr{}
	}
	return addr.Unmap()
}

// HTTPS reports whether the client used TLS directly or via a configured gateway.
// An authorized gateway must overwrite X-Forwarded-Proto on every request.
func HTTPS(r *http.Request, trusted []netip.Prefix) bool {
	if r.TLS != nil {
		return true
	}
	forwarded := r.Header.Values("X-Forwarded-Proto")
	if len(forwarded) != 1 || forwarded[0] != "https" {
		return false
	}
	peer := Peer(r)
	for _, proxy := range trusted {
		if proxy.Contains(peer) {
			return true
		}
	}
	return false
}
