package safehttp

import (
	"net"
	"strings"
	"testing"
	"time"
)

func TestClientRefusesPrivateAddresses(t *testing.T) {
	client := Client(2 * time.Second)
	// Literal private/loopback/metadata IPs must be refused at dial time.
	for _, target := range []string{
		"http://127.0.0.1/",
		"http://169.254.169.254/latest/meta-data/", // cloud metadata
		"http://10.0.0.1/",
		"http://100.64.0.1/", // CGNAT
	} {
		resp, err := client.Get(target)
		if err == nil {
			resp.Body.Close()
			t.Errorf("%s: expected dial to be refused, got %s", target, resp.Status)
			continue
		}
		if !strings.Contains(err.Error(), "non-public") {
			t.Errorf("%s: refused for the wrong reason: %v", target, err)
		}
	}
}

func TestIsPublic(t *testing.T) {
	cases := map[string]bool{
		"8.8.8.8":         true,
		"1.1.1.1":         true,
		"127.0.0.1":       false,
		"10.1.2.3":        false,
		"192.168.1.1":     false,
		"172.16.0.1":      false,
		"169.254.169.254": false,
		"100.64.0.1":      false,
		"::1":             false,
		"fc00::1":         false,
		"2606:4700::1111": true,
	}
	for s, want := range cases {
		if got := isPublic(net.ParseIP(s)); got != want {
			t.Errorf("isPublic(%s) = %v, want %v", s, got, want)
		}
	}
}

// control is exercised indirectly above; this guards the address-parse path.
func TestControlRejectsUnparseable(t *testing.T) {
	if err := control("tcp", "not-an-address", nil); err == nil {
		t.Fatal("expected error for unparseable address")
	}
}
