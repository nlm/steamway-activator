// Package wol parses Wake-on-LAN magic packets.
package wol

import (
	"bytes"
	"net"
)

const macLen = 6

// ParseMagic returns the MAC of a magic packet: six 0xFF bytes followed by
// one MAC repeated 16 times. Trailing bytes (a SecureOn password) are allowed.
func ParseMagic(pkt []byte) (net.HardwareAddr, bool) {
	const size = macLen * 17
	if len(pkt) < size || !bytes.Equal(pkt[:macLen], bytes.Repeat([]byte{0xff}, macLen)) {
		return nil, false
	}
	mac := pkt[macLen : 2*macLen]
	for i := 2; i <= 16; i++ {
		if !bytes.Equal(pkt[i*macLen:(i+1)*macLen], mac) {
			return nil, false
		}
	}
	return net.HardwareAddr(bytes.Clone(mac)), true
}
