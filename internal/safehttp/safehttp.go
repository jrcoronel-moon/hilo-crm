// Package safehttp fetches caller-chosen URLs without letting them reach
// inside the network (SSRF): every connection, redirects included, goes to a
// public address on port 443, checked after DNS resolution.
package safehttp

import (
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"syscall"
	"time"
)

// blockedPrefixes are non-public ranges not covered by netip's predicates.
var blockedPrefixes = []netip.Prefix{
	netip.MustParsePrefix("0.0.0.0/8"),
	netip.MustParsePrefix("100.64.0.0/10"), // carrier-grade NAT
	netip.MustParsePrefix("192.0.0.0/24"),
	netip.MustParsePrefix("192.0.2.0/24"),
	netip.MustParsePrefix("198.18.0.0/15"),
	netip.MustParsePrefix("198.51.100.0/24"),
	netip.MustParsePrefix("203.0.113.0/24"),
	netip.MustParsePrefix("240.0.0.0/4"),
	netip.MustParsePrefix("64:ff9b::/96"), // NAT64 can reach IPv4 private space
	netip.MustParsePrefix("64:ff9b:1::/48"),
	netip.MustParsePrefix("2001:db8::/32"),
}

// PublicAddr reports whether a is a globally routable unicast address.
func PublicAddr(a netip.Addr) bool {
	a = a.Unmap()
	if !a.IsValid() || !a.IsGlobalUnicast() || a.IsPrivate() || a.IsLoopback() || a.IsLinkLocalUnicast() ||
		a.IsMulticast() || a.IsUnspecified() || a.IsInterfaceLocalMulticast() || a.IsLinkLocalMulticast() {
		return false
	}
	for _, p := range blockedPrefixes {
		if p.Contains(a) {
			return false
		}
	}
	return true
}

// dialControl runs after DNS resolution, right before connecting, so a name
// that resolves (or re-resolves) to an internal address is refused.
func dialControl(_, address string, _ syscall.RawConn) error {
	host, port, err := net.SplitHostPort(address)
	if err != nil {
		return err
	}
	a, err := netip.ParseAddr(host)
	if err != nil || !PublicAddr(a) || port != "443" {
		return fmt.Errorf("refusing to connect to non-public address %s", address)
	}
	return nil
}

// Client returns an HTTPS-only client bounded by timeout that follows at most
// maxRedirects redirects (0: none), each one to an https URL and through the
// same address checks.
func Client(timeout time.Duration, maxRedirects int) *http.Client {
	return &http.Client{
		Timeout: timeout,
		Transport: &http.Transport{
			Proxy:                  nil,
			DialContext:            (&net.Dialer{Timeout: timeout, Control: dialControl}).DialContext,
			TLSHandshakeTimeout:    timeout,
			ResponseHeaderTimeout:  timeout,
			MaxResponseHeaderBytes: 16 << 10,
			DisableKeepAlives:      true,
		},
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) > maxRedirects {
				return errors.New("redirects not allowed")
			}
			if req.URL.Scheme != "https" {
				return errors.New("redirect to a non-https URL")
			}
			return nil
		},
	}
}
