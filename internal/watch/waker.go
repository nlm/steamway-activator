package watch

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"sync"
	"time"

	"steamway-activator/internal/scw"
	"steamway-activator/internal/wol"
)

// Outcome is what Handle did with a packet.
type Outcome int

const (
	Ignored  Outcome = iota // not a magic packet for our MAC
	Guarded                 // a poweron was sent moments ago
	Starting                // the instance is already starting
	Activity                // the instance is running; idle clock reset
	Pending                 // the instance is stopping; it will be started once stopped
	Locked                  // Scaleway locked the instance
	Started                 // poweron sent
	Failed                  // State or PowerOn failed
)

var outcomeNames = [...]string{"Ignored", "Guarded", "Starting", "Activity", "Pending", "Locked", "Started", "Failed"}

func (o Outcome) String() string {
	if o < 0 || int(o) >= len(outcomeNames) {
		return fmt.Sprintf("Outcome(%d)", int(o))
	}
	return outcomeNames[o]
}

// Waker starts the instance on a Wake-on-LAN packet for its MAC, by state.
type Waker struct {
	MAC        net.HardwareAddr
	Instance   Instance
	Notify     Notifier
	MarkActive func() // Checker.MarkActive in production; may be nil
	// Guard, PollInterval and PendingTimeout are 10 s, 10 s and 3 min in production.
	Guard, PollInterval, PendingTimeout time.Duration
	Now                                 func() time.Time
	Log                                 *slog.Logger
	// Context bounds the pending-wait goroutine (cancel it on program
	// shutdown). Nil means context.Background().
	Context context.Context

	mu        sync.Mutex // guards the fields below, never held across a network call
	lastStart time.Time
	inflight  bool // a PowerOn call is in progress
	pending   bool
	wg        sync.WaitGroup
}

// Handle processes one UDP packet.
func (w *Waker) Handle(ctx context.Context, pkt []byte) Outcome {
	mac, ok := wol.ParseMagic(pkt)
	if !ok || !bytes.Equal(mac, w.MAC) {
		return Ignored
	}
	out := w.handle(ctx)
	w.Log.Info("wake packet", "outcome", out.String())
	return out
}

func (w *Waker) handle(ctx context.Context) Outcome {
	w.mu.Lock()
	guarded := w.guarded()
	w.mu.Unlock()
	if guarded {
		return Guarded
	}

	state, err := w.Instance.State(ctx)
	if err != nil {
		w.stateFailed(err)
		return Failed
	}
	switch state {
	case "stopped", "stopped in place":
		return w.powerOn(ctx)
	case "starting":
		return Starting
	case "running":
		if w.MarkActive != nil {
			w.MarkActive()
		}
		return Activity
	case "stopping":
		w.mu.Lock()
		if w.pending {
			w.mu.Unlock()
			return Pending
		}
		w.pending = true
		w.wg.Add(1)
		w.mu.Unlock()
		w.notify("Instance was shutting down; it will start again")
		go w.waitStopped()
		return Pending
	case "locked":
		w.notify("Instance is locked by Scaleway, can't start")
		return Locked
	default:
		w.Log.Warn("unexpected instance state", "state", state)
		return Failed
	}
}

// guarded reports whether a poweron is in progress or was sent within
// Guard. The caller holds w.mu.
func (w *Waker) guarded() bool {
	return w.inflight || (!w.lastStart.IsZero() && w.Now().Sub(w.lastStart) < w.Guard)
}

// powerOn sends poweron and arms the guard on success. The start is reserved
// under the mutex before the call, so concurrent callers (packets, the
// pending wait) send at most one poweron; a failure leaves no guard.
func (w *Waker) powerOn(ctx context.Context) Outcome {
	w.mu.Lock()
	if w.guarded() {
		w.mu.Unlock()
		return Guarded
	}
	w.inflight = true
	w.mu.Unlock()

	err := w.Instance.PowerOn(ctx)

	w.mu.Lock()
	w.inflight = false
	if err == nil {
		w.lastStart = w.Now()
	}
	w.mu.Unlock()
	if err != nil {
		w.Log.Error("power on failed", "err", err)
		w.notify("Instance didn't start: " + errText(err))
		return Failed
	}
	w.notify("Starting the instance…")
	return Started
}

func (w *Waker) stateFailed(err error) {
	w.Log.Warn("scaleway state failed", "err", err)
	w.notify("Can't reach Scaleway to start the instance: " + errText(err))
}

// waitStopped polls until the instance is stopped, then starts it. It gives
// up silently on any other state, ignores State errors, and with a notification after PendingTimeout.
func (w *Waker) waitStopped() {
	defer w.wg.Done()
	defer func() {
		w.mu.Lock()
		w.pending = false
		w.mu.Unlock()
	}()
	ctx := w.Context
	if ctx == nil {
		ctx = context.Background()
	}
	timeout := time.NewTimer(w.PendingTimeout)
	defer timeout.Stop()
	tick := time.NewTicker(w.PollInterval)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-timeout.C:
			w.Log.Warn("instance still stopping", "after", w.PendingTimeout)
			w.notify("Instance didn't finish shutting down in 3 min; press Wake PC again")
			return
		case <-tick.C:
		}
		state, err := w.Instance.State(ctx)
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			// No answer: keep polling; the timeout notification covers lasting outages.
			w.Log.Warn("scaleway state failed while waiting", "err", err)
			continue
		}
		switch state {
		case "stopping":
		case "stopped", "stopped in place":
			w.Log.Info("pending wake", "outcome", w.powerOn(ctx).String())
			return
		default:
			w.Log.Info("pending wake dropped", "state", state)
			return
		}
	}
}

// Wait blocks until no pending-wait goroutine is running.
func (w *Waker) Wait() { w.wg.Wait() }

func (w *Waker) notify(msg string) {
	if w.Notify != nil {
		w.Notify("steamway-activator", msg)
	}
}

// errText is the API message for API errors, else the error text.
func errText(err error) string {
	if ae, ok := errors.AsType[*scw.APIError](err); ok {
		return ae.Message
	}
	return err.Error()
}
