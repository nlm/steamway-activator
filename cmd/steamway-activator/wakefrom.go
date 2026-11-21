package main

import (
	"fmt"
	"net"
	"net/netip"
	"strings"
)

// parseWakeFrom parses the --wake-from value: a comma-separated list of IP
// addresses and CIDRs (IPv4 or IPv6), or the single value "any". The returned
// matcher covers only these extra sources; local addresses are accepted
// separately. An empty value matches nothing. Mixing "any" with other entries
// is an error.
func parseWakeFrom(s string) (func(net.IP) bool, error) {
	if strings.TrimSpace(s) == "" {
		return func(net.IP) bool { return false }, nil
	}
	entries := strings.Split(s, ",")
	var prefixes []netip.Prefix
	anyCount := 0
	for _, e := range entries {
		e = strings.TrimSpace(e)
		switch {
		case strings.EqualFold(e, "any"):
			anyCount++
		case strings.Contains(e, "/"):
			p, err := netip.ParsePrefix(e)
			if err != nil {
				return nil, fmt.Errorf("invalid --wake-from entry %q: %w", e, err)
			}
			prefixes = append(prefixes, p.Masked())
		default:
			a, err := netip.ParseAddr(e)
			if err != nil {
				return nil, fmt.Errorf("invalid --wake-from entry %q: not an IP address, a CIDR or \"any\"", e)
			}
			prefixes = append(prefixes, netip.PrefixFrom(a.Unmap(), a.Unmap().BitLen()))
		}
	}
	if anyCount > 0 {
		if anyCount != len(entries) || anyCount > 1 {
			return nil, fmt.Errorf("invalid --wake-from %q: \"any\" cannot be combined with other entries", s)
		}
		return func(net.IP) bool { return true }, nil
	}
	return func(ip net.IP) bool {
		a, ok := netip.AddrFromSlice(ip)
		if !ok {
			return false
		}
		a = a.Unmap().WithZone("")
		for _, p := range prefixes {
			if p.Contains(a) {
				return true
			}
		}
		return false
	}, nil
}
