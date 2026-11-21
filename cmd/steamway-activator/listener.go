package main

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"time"
)

const (
	listenRetry  = 60 * time.Second
	listenNotice = time.Hour
)

// listenSupervisor keeps the Wake listener alive for as long as ctx lives. The
// automatic shutdown check does not depend on it, so a failing listener must
// never take the process down.
type listenSupervisor struct {
	// Listen binds the socket. Serve reads packets from it until it is closed,
	// ctx is done (nil) or it fails (error).
	Listen func() (net.PacketConn, error)
	Serve  func(ctx context.Context, pc net.PacketConn) error
	Notify func(message string)
	Retry  time.Duration // wait before binding again
	Now    func() time.Time
	Log    *slog.Logger
	// Bound, if set, is called after each successful bind.
	Bound func(pc net.PacketConn)
}

// Run blocks until ctx is done. The socket is closed before it returns.
func (s *listenSupervisor) Run(ctx context.Context) {
	var lastNotice time.Time
	for ctx.Err() == nil {
		err := s.once(ctx)
		if ctx.Err() != nil {
			return
		}
		if err == nil {
			err = fmt.Errorf("listener stopped unexpectedly")
		}
		s.Log.Error("wake listener failed, will retry", "err", err, "retry_in", s.Retry)
		if now := s.Now(); lastNotice.IsZero() || now.Sub(lastNotice) >= listenNotice {
			lastNotice = now
			s.Notify(fmt.Sprintf("Wake PC is disabled: can't listen on UDP 9 — %v; automatic shutdown still works", err))
		}
		t := time.NewTimer(s.Retry)
		select {
		case <-ctx.Done():
			t.Stop()
			return
		case <-t.C:
		}
	}
}

// once binds and serves until failure or shutdown.
func (s *listenSupervisor) once(ctx context.Context) error {
	pc, err := s.Listen()
	if err != nil {
		return err
	}
	defer pc.Close()
	if s.Bound != nil {
		s.Bound(pc)
	}
	// Unblocks the read on shutdown.
	defer context.AfterFunc(ctx, func() { pc.Close() })()
	return s.Serve(ctx, pc)
}
