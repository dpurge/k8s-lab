package server

import (
	"net/netip"
	"strings"
	"testing"
)

func TestStripHTMLRemovesScriptAndStyleBlocks(t *testing.T) {
	source := `<p>before</p><script>if (a < b) { console.log("x"); }</script><style>body{color:red}</style><p>after</p>`
	got := stripHTML(source)
	for _, unwanted := range []string{"console", "color:red", "<", ">"} {
		if strings.Contains(got, unwanted) {
			t.Errorf("stripHTML(%q) = %q, want no occurrence of %q", source, got, unwanted)
		}
	}
	if !strings.Contains(got, "before") || !strings.Contains(got, "after") {
		t.Errorf("stripHTML(%q) = %q, want surrounding text preserved", source, got)
	}
}

func TestStripHTMLConvertsBlockTagsToNewlines(t *testing.T) {
	source := "<p>First paragraph.</p><p>Second paragraph.</p>Line one<br>Line two"
	got := stripHTML(source)
	want := "First paragraph.\nSecond paragraph.\nLine one\nLine two"
	if got != want {
		t.Errorf("stripHTML(%q) = %q, want %q", source, got, want)
	}
}

func TestStripHTMLPlainTextPassesThroughUnchanged(t *testing.T) {
	source := "Just plain text with no markup at all."
	got := stripHTML(source)
	if got != source {
		t.Errorf("stripHTML(%q) = %q, want unchanged", source, got)
	}
}

// TestIsDisallowedIngestTarget covers the IP-classification logic
// ingestDialer's Control hook uses to block SSRF against internal/metadata
// infrastructure, tested directly against netip.Addr values rather than via
// a real network round-trip.
func TestIsDisallowedIngestTarget(t *testing.T) {
	cases := []struct {
		name string
		addr string
		want bool
	}{
		{"IPv4 loopback", "127.0.0.1", true},
		{"IPv6 loopback", "::1", true},
		{"IPv4-mapped IPv6 loopback", "::ffff:127.0.0.1", true},
		{"RFC1918 10/8", "10.0.0.5", true},
		{"RFC1918 172.16/12", "172.16.0.1", true},
		{"RFC1918 192.168/16", "192.168.1.1", true},
		{"IPv6 unique local (RFC4193)", "fc00::1", true},
		{"link-local unicast", "169.254.1.1", true},
		{"cloud metadata address", "169.254.169.254", true},
		{"unspecified IPv4", "0.0.0.0", true},
		{"unspecified IPv6", "::", true},
		{"public IPv4", "8.8.8.8", false},
		{"public IPv6", "2606:4700:4700::1111", false},
	}
	for _, c := range cases {
		addr, err := netip.ParseAddr(c.addr)
		if err != nil {
			t.Fatalf("netip.ParseAddr(%q): %v", c.addr, err)
		}
		if got := isDisallowedIngestTarget(addr); got != c.want {
			t.Errorf("isDisallowedIngestTarget(%s) [%s] = %v, want %v", c.addr, c.name, got, c.want)
		}
	}
}
