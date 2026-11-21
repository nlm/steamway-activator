package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"time"
)

const (
	readBackoff     = 100 * time.Millisecond
	readMaxFailures = 20
)

// packetReader is the part of net.PacketConn the reader loop needs.
type packetReader interface {
	ReadFrom(p []byte) (int, net.Addr, error)
}

// readLoop passes each received packet from a local source to handle, synchronously. It returns
// nil when the connection was closed or ctx is done, and an error after
// maxFailures consecutive read errors. Any other read error is retried after
// backoff.
func readLoop(ctx context.Context, pc packetReader, handle func(pkt []byte), isLocal func(net.IP) bool, log *slog.Logger, backoff time.Duration, maxFailures int) error {
	buf := make([]byte, 1500)
	fails := 0
	for {
		n, src, err := pc.ReadFrom(buf)
		if err == nil {
			fails = 0
			if ip := addrIP(src); ip == nil || !isLocal(ip) {
				log.Debug("dropping packet from non-local source", "src", fmt.Sprint(src))
				continue
			}
			handle(buf[:n])
			continue
		}
		if errors.Is(err, net.ErrClosed) || ctx.Err() != nil {
			return nil
		}
		fails++
		if fails >= maxFailures {
			return fmt.Errorf("udp read failed %d times in a row: %w", fails, err)
		}
		log.Warn("udp read failed, retrying", "err", err, "consecutive", fails)
		select {
		case <-ctx.Done():
			return nil
		case <-time.After(backoff):
		}
	}
}

func addrIP(a net.Addr) net.IP {
	if ua, ok := a.(*net.UDPAddr); ok {
		return ua.IP
	}
	return nil
}

// isLocalIP reports whether ip is loopback or one of this machine's current
// interface addresses. Addresses are queried on every call as DHCP and Wi-Fi
// change them.
func isLocalIP(ip net.IP) bool {
	if ip.IsLoopback() {
		return true
	}
	addrs, err := net.InterfaceAddrs()
	if err != nil {
		return false
	}
	for _, a := range addrs {
		if ipn, ok := a.(*net.IPNet); ok && ipn.IP.Equal(ip) {
			return true
		}
	}
	return false
}
