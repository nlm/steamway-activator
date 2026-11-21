package main

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net"
	"testing"
	"time"
)

type step struct {
	pkt string
	src string
	err error
}

type fakeReader struct{ steps []step }

func (f *fakeReader) ReadFrom(p []byte) (int, net.Addr, error) {
	s := f.steps[0]
	f.steps = f.steps[1:]
	if s.err != nil {
		return 0, nil, s.err
	}
	src := s.src
	if src == "" {
		src = "127.0.0.1"
	}
	return copy(p, s.pkt), &net.UDPAddr{IP: net.ParseIP(src), Port: 1234}, nil
}

func all(net.IP) bool { return true }

var quiet = slog.New(slog.NewTextHandler(io.Discard, nil))

func TestReadLoop_TransientThenPacket(t *testing.T) {
	r := &fakeReader{steps: []step{{err: errors.New("hiccup")}, {pkt: "abc"}, {err: net.ErrClosed}}}
	var got []string
	err := readLoop(context.Background(), r, func(p []byte) { got = append(got, string(p)) }, all, quiet, time.Millisecond, 3)
	if err != nil || len(got) != 1 || got[0] != "abc" {
		t.Fatalf("err=%v got=%v", err, got)
	}
}

func TestReadLoop_ConsecutiveFailures(t *testing.T) {
	e := errors.New("boom")
	r := &fakeReader{steps: []step{{err: e}, {err: e}, {err: e}}}
	err := readLoop(context.Background(), r, func([]byte) {}, all, quiet, time.Millisecond, 3)
	if !errors.Is(err, e) {
		t.Fatalf("err=%v", err)
	}
}

func TestReadLoop_SuccessResetsFailures(t *testing.T) {
	e := errors.New("boom")
	r := &fakeReader{steps: []step{{err: e}, {err: e}, {pkt: "x"}, {err: e}, {err: e}, {err: net.ErrClosed}}}
	if err := readLoop(context.Background(), r, func([]byte) {}, all, quiet, time.Millisecond, 3); err != nil {
		t.Fatal(err)
	}
}

func TestReadLoop_ClosedIsClean(t *testing.T) {
	r := &fakeReader{steps: []step{{err: net.ErrClosed}}}
	if err := readLoop(context.Background(), r, func([]byte) {}, all, quiet, time.Millisecond, 3); err != nil {
		t.Fatal(err)
	}
}

func TestReadLoop_SourceFilter(t *testing.T) {
	r := &fakeReader{steps: []step{{pkt: "evil", src: "203.0.113.7"}, {pkt: "ok", src: "127.0.0.1"}, {err: net.ErrClosed}}}
	var got []string
	err := readLoop(context.Background(), r, func(p []byte) { got = append(got, string(p)) }, isLocalIP, quiet, time.Millisecond, 3)
	if err != nil || len(got) != 1 || got[0] != "ok" {
		t.Fatalf("err=%v got=%v", err, got)
	}
}

func TestIsLocalIP(t *testing.T) {
	if !isLocalIP(net.ParseIP("127.0.0.1")) || isLocalIP(net.ParseIP("203.0.113.7")) {
		t.Fatal("wrong")
	}
}
