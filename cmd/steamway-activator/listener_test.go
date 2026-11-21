package main

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net"
	"sync"
	"testing"
	"time"
)

type fakePC struct {
	net.PacketConn
	closed chan struct{}
	once   sync.Once
}

func newFakePC() *fakePC { return &fakePC{closed: make(chan struct{})} }
func (f *fakePC) Close() error {
	f.once.Do(func() { close(f.closed) })
	return nil
}
func (f *fakePC) LocalAddr() net.Addr { return &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 9} }

type supEnv struct {
	mu    sync.Mutex
	notes []string
	sup   *listenSupervisor
}

func (e *supEnv) notified() []string {
	e.mu.Lock()
	defer e.mu.Unlock()
	return append([]string(nil), e.notes...)
}

func newSupEnv(listen func() (net.PacketConn, error), serve func(context.Context, net.PacketConn) error) *supEnv {
	e := &supEnv{}
	e.sup = &listenSupervisor{
		Listen: listen, Serve: serve,
		Notify: func(m string) { e.mu.Lock(); e.notes = append(e.notes, m); e.mu.Unlock() },
		Retry:  time.Millisecond,
		Now:    time.Now,
		Log:    slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
	return e
}

func runSup(ctx context.Context, e *supEnv) <-chan struct{} {
	done := make(chan struct{})
	go func() { defer close(done); e.sup.Run(ctx) }()
	return done
}

func waitFor(t *testing.T, ch <-chan struct{}, what string) {
	t.Helper()
	select {
	case <-ch:
	case <-time.After(5 * time.Second):
		t.Fatal("timeout waiting for " + what)
	}
}

func TestSupervisor_BindFailsThenServes(t *testing.T) {
	var mu sync.Mutex
	attempts := 0
	served := make(chan struct{})
	e := newSupEnv(func() (net.PacketConn, error) {
		mu.Lock()
		defer mu.Unlock()
		attempts++
		if attempts <= 2 {
			return nil, errors.New("address already in use")
		}
		return newFakePC(), nil
	}, func(ctx context.Context, pc net.PacketConn) error {
		close(served)
		<-ctx.Done()
		return nil
	})
	ctx, cancel := context.WithCancel(context.Background())
	done := runSup(ctx, e)
	waitFor(t, served, "serve after retries")
	cancel()
	waitFor(t, done, "supervisor exit")
	mu.Lock()
	defer mu.Unlock()
	if attempts != 3 {
		t.Errorf("attempts %d, want 3", attempts)
	}
	notes := e.notified()
	if len(notes) != 1 {
		t.Fatalf("notifications %q, want exactly one", notes)
	}
	want := "Wake PC is disabled: can't listen on UDP 9 — address already in use; automatic shutdown still works"
	if notes[0] != want {
		t.Errorf("note %q, want %q", notes[0], want)
	}
}

func TestSupervisor_HandlesPacketsAfterRebind(t *testing.T) {
	// Real sockets: the port is busy at first, then freed.
	busy, err := net.ListenPacket("udp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := busy.LocalAddr().String()
	got := make(chan string, 1)
	e := newSupEnv(func() (net.PacketConn, error) { return net.ListenPacket("udp4", addr) },
		func(ctx context.Context, pc net.PacketConn) error {
			return readLoop(ctx, pc, func(p []byte) { got <- string(p) }, func(net.IP) bool { return true },
				slog.New(slog.NewTextHandler(io.Discard, nil)), time.Millisecond, 3)
		})
	ctx, cancel := context.WithCancel(context.Background())
	done := runSup(ctx, e)
	deadline := time.Now().Add(5 * time.Second)
	for len(e.notified()) == 0 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	busy.Close()
	c, err := net.Dial("udp4", addr)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	for {
		c.Write([]byte("hello"))
		select {
		case m := <-got:
			if m != "hello" {
				t.Fatalf("got %q", m)
			}
			cancel()
			waitFor(t, done, "supervisor exit")
			if n := e.notified(); len(n) != 1 {
				t.Errorf("notifications %q, want one", n)
			}
			return
		case <-time.After(20 * time.Millisecond):
			if time.Now().After(deadline) {
				t.Fatal("no packet handled after the port was freed")
			}
		}
	}
}

func TestSupervisor_ServeErrorRebinds(t *testing.T) {
	var mu sync.Mutex
	var pcs []*fakePC
	second := make(chan struct{})
	e := newSupEnv(func() (net.PacketConn, error) {
		mu.Lock()
		defer mu.Unlock()
		pc := newFakePC()
		pcs = append(pcs, pc)
		return pc, nil
	}, func(ctx context.Context, pc net.PacketConn) error {
		mu.Lock()
		n := len(pcs)
		mu.Unlock()
		if n == 1 {
			return errors.New("udp read failed 20 times in a row")
		}
		close(second)
		<-ctx.Done()
		return nil
	})
	ctx, cancel := context.WithCancel(context.Background())
	done := runSup(ctx, e)
	waitFor(t, second, "second bind")
	cancel()
	waitFor(t, done, "supervisor exit")
	mu.Lock()
	defer mu.Unlock()
	if len(pcs) != 2 {
		t.Fatalf("binds %d, want 2", len(pcs))
	}
	for i, pc := range pcs {
		select {
		case <-pc.closed:
		default:
			t.Errorf("socket %d not closed", i)
		}
	}
	if n := e.notified(); len(n) != 1 {
		t.Errorf("notifications %q, want one", n)
	}
}

func TestSupervisor_CancelDuringRetryWait(t *testing.T) {
	e := newSupEnv(func() (net.PacketConn, error) { return nil, errors.New("busy") }, nil)
	e.sup.Retry = time.Hour
	ctx, cancel := context.WithCancel(context.Background())
	done := runSup(ctx, e)
	deadline := time.Now().Add(5 * time.Second)
	for len(e.notified()) == 0 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	cancel()
	waitFor(t, done, "prompt return from the retry wait")
}

func TestSupervisor_NotifiesHourly(t *testing.T) {
	now := time.Date(2026, 11, 21, 20, 0, 0, 0, time.UTC)
	var mu sync.Mutex
	tries := make(chan struct{}, 10)
	e := newSupEnv(func() (net.PacketConn, error) {
		tries <- struct{}{}
		return nil, errors.New("busy")
	}, nil)
	e.sup.Now = func() time.Time { mu.Lock(); defer mu.Unlock(); return now }
	ctx, cancel := context.WithCancel(context.Background())
	done := runSup(ctx, e)
	for range 3 {
		waitFor(t, func() <-chan struct{} {
			c := make(chan struct{})
			go func() { <-tries; close(c) }()
			return c
		}(), "bind attempt")
	}
	mu.Lock()
	now = now.Add(61 * time.Minute)
	mu.Unlock()
	for range 3 {
		<-tries
	}
	cancel()
	waitFor(t, done, "supervisor exit")
	if n := e.notified(); len(n) != 2 {
		t.Errorf("notifications %d, want 2 (one per hour)", len(n))
	}
}
