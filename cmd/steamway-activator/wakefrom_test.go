package main

import (
	"net"
	"testing"
)

func TestParseWakeFrom(t *testing.T) {
	tests := []struct {
		name    string
		value   string
		src     string
		want    bool
		wantErr bool
	}{
		{"empty matches nothing", "", "192.168.1.5", false, false},
		{"cidr match", "192.168.1.0/24", "192.168.1.77", true, false},
		{"cidr non-match", "192.168.1.0/24", "192.168.2.77", false, false},
		{"cidr with host bits", "10.8.0.5/24", "10.8.0.99", true, false},
		{"single ip match", "10.8.0.5", "10.8.0.5", true, false},
		{"single ip non-match", "10.8.0.5", "10.8.0.6", false, false},
		{"list", "192.168.1.0/24, 10.8.0.5", "10.8.0.5", true, false},
		{"ipv4-mapped source in ipv4 cidr", "192.168.1.0/24", "::ffff:192.168.1.9", true, false},
		{"ipv4-mapped single ip entry", "::ffff:10.8.0.5", "10.8.0.5", true, false},
		{"ipv6 cidr match", "fd00::/8", "fd12::1", true, false},
		{"ipv6 cidr non-match", "fd00::/8", "fe80::1", false, false},
		{"ipv6 does not match ipv4 cidr", "192.168.1.0/24", "fd00::1", false, false},
		{"any", "any", "8.8.8.8", true, false},
		{"any uppercase", "ANY", "2001:db8::1", true, false},
		{"bad ip", "300.1.1.1", "", false, true},
		{"bad prefix length", "10.0.0.0/33", "", false, true},
		{"garbage", "foo", "", false, true},
		{"any mixed", "any,10.0.0.1", "", false, true},
		{"mixed any last", "10.0.0.1,any", "", false, true},
		{"any twice", "any,any", "", false, true},
		{"empty entry", "10.0.0.1,,10.0.0.2", "", false, true},
		{"trailing comma", "10.0.0.1,", "", false, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			allow, err := parseWakeFrom(tt.value)
			if (err != nil) != tt.wantErr {
				t.Fatalf("parseWakeFrom(%q) err = %v, wantErr %v", tt.value, err, tt.wantErr)
			}
			if tt.wantErr {
				return
			}
			ip := net.ParseIP(tt.src)
			if ip == nil {
				t.Fatalf("bad test source %q", tt.src)
			}
			if got := allow(ip); got != tt.want {
				t.Errorf("allow(%s) = %v, want %v", tt.src, got, tt.want)
			}
		})
	}
}

func TestWakeFromKeepsLocal(t *testing.T) {
	allow, err := parseWakeFrom("192.0.2.0/24")
	if err != nil {
		t.Fatal(err)
	}
	accept := func(ip net.IP) bool { return isLocalIP(ip) || allow(ip) }
	if !accept(net.ParseIP("127.0.0.1")) {
		t.Error("loopback must always be accepted")
	}
	if accept(net.ParseIP("198.51.100.1")) {
		t.Error("unlisted remote source must be rejected")
	}
}
