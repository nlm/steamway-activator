package wol

import (
	"bytes"
	"net"
	"testing"
)

var testMAC = net.HardwareAddr{0xde, 0x00, 0x00, 0xfe, 0x1d, 0x38}

func packet(mac net.HardwareAddr) []byte {
	p := bytes.Repeat([]byte{0xff}, 6)
	for range 16 {
		p = append(p, mac...)
	}
	return p
}

func TestParseMagic_Valid(t *testing.T) {
	p := packet(testMAC)
	if len(p) != 102 {
		t.Fatalf("helper built %d bytes", len(p))
	}
	got, ok := ParseMagic(p)
	if !ok || !bytes.Equal(got, testMAC) {
		t.Fatalf("got %v, %v", got, ok)
	}
}

func TestParseMagic_TrailingBytes(t *testing.T) {
	p := append(packet(testMAC), 1, 2, 3, 4, 5, 6)
	got, ok := ParseMagic(p)
	if !ok || !bytes.Equal(got, testMAC) {
		t.Fatalf("got %v, %v", got, ok)
	}
}

func TestParseMagic_Invalid(t *testing.T) {
	short := packet(testMAC)[:101]
	badHeader := packet(testMAC)
	badHeader[3] = 0xfe
	badRepeat := packet(testMAC)
	badRepeat[6+5*6+2] ^= 1
	for name, p := range map[string][]byte{
		"short": short, "bad header": badHeader, "bad repetition": badRepeat, "empty": nil,
	} {
		if got, ok := ParseMagic(p); ok {
			t.Errorf("%s: accepted, mac %v", name, got)
		}
	}
}
