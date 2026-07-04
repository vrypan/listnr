// Package safehttp builds HTTP clients for outbound federation requests that
// refuse to connect to non-public IP addresses. Inbox activities carry
// attacker-controlled URLs (keyId, actor id, remote inboxes); without this a
// crafted activity could make listnr fetch internal services or cloud
// metadata endpoints (SSRF). The guard runs at dial time, so it also covers
// every redirect hop and defeats DNS names that resolve to private space.
package safehttp

import (
	"errors"
	"fmt"
	"net"
	"net/http"
	"syscall"
	"time"
)

// control rejects a dial whose resolved address is not a public unicast IP.
func control(_, address string, _ syscall.RawConn) error {
	host, _, err := net.SplitHostPort(address)
	if err != nil {
		return err
	}
	ip := net.ParseIP(host)
	if ip == nil {
		return fmt.Errorf("cannot parse dial address %q", address)
	}
	if !isPublic(ip) {
		return fmt.Errorf("refusing to dial non-public address %s", ip)
	}
	return nil
}

// cgnat is the 100.64.0.0/10 carrier-grade NAT range, which net.IP.IsPrivate
// does not cover.
var cgnat = &net.IPNet{IP: net.IPv4(100, 64, 0, 0), Mask: net.CIDRMask(10, 32)}

func isPublic(ip net.IP) bool {
	if ip.IsLoopback() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() ||
		ip.IsInterfaceLocalMulticast() || ip.IsMulticast() || ip.IsUnspecified() ||
		ip.IsPrivate() {
		return false
	}
	if cgnat.Contains(ip) {
		return false
	}
	return true
}

// Client returns an http.Client for outbound federation traffic: the dial
// guard above, a redirect cap, and a non-http(s) redirect refusal.
func Client(timeout time.Duration) *http.Client {
	dialer := &net.Dialer{
		Timeout:   10 * time.Second,
		KeepAlive: 30 * time.Second,
		Control:   control,
	}
	transport := &http.Transport{
		Proxy:                 http.ProxyFromEnvironment,
		DialContext:           dialer.DialContext,
		ForceAttemptHTTP2:     true,
		MaxIdleConns:          100,
		IdleConnTimeout:       90 * time.Second,
		TLSHandshakeTimeout:   10 * time.Second,
		ExpectContinueTimeout: time.Second,
	}
	return &http.Client{
		Timeout:   timeout,
		Transport: transport,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= 5 {
				return errors.New("too many redirects")
			}
			if req.URL.Scheme != "http" && req.URL.Scheme != "https" {
				return fmt.Errorf("refusing non-http redirect to %q", req.URL.Scheme)
			}
			return nil
		},
	}
}
