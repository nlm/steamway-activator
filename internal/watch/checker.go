// Package watch decides when to power the instance off. It only watches an
// instance after a Wake PC: Check does nothing until Arm (the waker powered it
// on) or MarkActive (Wake PC while it was already running) is called, and
// stops again once the instance is powered off or seen stopped.
package watch

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"steamway-activator/internal/scw"
)

// Instance is the Scaleway server the checker watches.
type Instance interface {
	State(ctx context.Context) (string, error)
	PowerOn(ctx context.Context) error
	PowerOff(ctx context.Context) error
}

// Sunshine reports the running app id (0 = none).
type Sunshine interface {
	CurrentGame(ctx context.Context) (int, error)
}

// Notifier shows a user notification.
type Notifier func(title, message string)

const noticeEvery = time.Hour

// startGrace is how long after Arm a "stopped"/"stopping" state is taken as
// Scaleway lag rather than the end of the session, until the instance is seen up.
const startGrace = 3 * time.Minute

// Checker applies the power-off rules once per Check call.
type Checker struct {
	Instance                              Instance
	Sunshine                              Sunshine
	Notify                                Notifier
	IdleLimit, UnreachableLimit, Interval time.Duration
	Now                                   func() time.Time
	Log                                   *slog.Logger

	mu               sync.Mutex // guards the fields below, never held across a network call
	armed            bool       // watching a session this program started
	armedAt          time.Time
	seenUp           bool // running or starting seen since Arm
	lastCheck        time.Time
	lastActive       time.Time
	unreachableSince time.Time
	lastAuthNotice   time.Time
	lastKeyNotice    time.Time
	lastNotFound     time.Time
	sawDown          bool // a non-running state was seen and no ready notice sent since
}

// Result describes one check. Reason is "idle", "unreachable",
// "stopped in place" or "" when no power off was decided.
type Result struct {
	State      string
	PoweredOff bool
	Reason     string
}

// Arm starts watching the instance: call it after this program powered it on.
// It resets the timers. It is safe to call from other goroutines.
func (c *Checker) Arm() {
	now := c.Now()
	c.mu.Lock()
	c.armed, c.armedAt, c.seenUp = true, now, false
	c.lastActive, c.unreachableSince = time.Time{}, time.Time{}
	c.sawDown = true
	c.mu.Unlock()
	c.Log.Info("watching instance")
}

// Armed reports whether the checker is watching the instance.
func (c *Checker) Armed() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.armed
}

// disarm stops watching. The caller holds c.mu.
func (c *Checker) disarm(why string) {
	c.armed = false
	c.lastActive, c.unreachableSince = time.Time{}, time.Time{}
	c.Log.Info("stopped watching instance", "why", why)
}

// MarkActive records a Wake PC on a running instance: it resets the idle
// clock and, if the checker isn't watching yet, starts watching (the instance
// was started elsewhere, e.g. from the console). It is safe to call from other
// goroutines.
func (c *Checker) MarkActive() {
	now := c.Now()
	c.mu.Lock()
	adopt := !c.armed
	if adopt {
		c.armed, c.armedAt, c.seenUp = true, now, true
		c.unreachableSince = time.Time{}
		c.sawDown = true // confirm with a ready notice at the next check
	}
	c.lastActive = now
	c.mu.Unlock()
	if adopt {
		c.Log.Info("watching instance", "via", "wake on running instance")
	}
}

// Check runs one check. It must only be called from the check loop. It makes
// no network call unless the checker is armed.
func (c *Checker) Check(ctx context.Context) Result {
	now := c.Now()

	c.mu.Lock()
	if !c.armed {
		c.lastCheck = now
		c.mu.Unlock()
		return Result{}
	}
	if !c.lastCheck.IsZero() && now.Sub(c.lastCheck) > 2*c.Interval {
		c.unreachableSince = time.Time{} // sleep gap
	}
	c.lastCheck = now
	c.mu.Unlock()

	state, err := c.Instance.State(ctx)
	if err != nil {
		c.stateError(now, err)
		return Result{}
	}
	res := Result{State: state}

	switch state {
	case "stopped in place":
		c.mu.Lock()
		c.sawDown = true
		c.mu.Unlock()
		return c.powerOff(ctx, res, "stopped in place", "it was stopped in place")
	case "running":
		c.mu.Lock()
		c.seenUp = true
		c.mu.Unlock()
	default:
		c.mu.Lock()
		c.sawDown = true
		c.lastActive, c.unreachableSince = time.Time{}, time.Time{}
		switch {
		case state == "starting":
			c.seenUp = true
		case state != "stopped" && state != "stopping":
			// locked or unknown: keep watching
		case c.seenUp:
			c.disarm("instance " + state)
		case now.Sub(c.armedAt) >= startGrace:
			c.disarm("instance still " + state + " after start")
		}
		c.mu.Unlock()
		return res
	}

	game, err := c.Sunshine.CurrentGame(ctx)
	if err != nil {
		c.Log.Warn("sunshine not answering", "err", err)
		c.mu.Lock()
		if c.unreachableSince.IsZero() {
			c.unreachableSince = now
		}
		off := now.Sub(c.unreachableSince) >= c.UnreachableLimit
		c.mu.Unlock()
		if off {
			return c.powerOff(ctx, res, "unreachable",
				fmt.Sprintf("Sunshine not answering for %s", c.UnreachableLimit))
		}
		return res
	}

	c.mu.Lock()
	c.unreachableSince = time.Time{}
	ready := c.sawDown
	c.sawDown = false
	off := false
	if game != 0 || c.lastActive.IsZero() {
		c.lastActive = now
	} else if now.Sub(c.lastActive) >= c.IdleLimit {
		off = true
	}
	c.mu.Unlock()
	if ready {
		c.notify("steamway-activator", "Instance ready: open Moonlight")
	}
	if off {
		return c.powerOff(ctx, res, "idle", fmt.Sprintf("no app running for %s", c.IdleLimit))
	}
	return res
}

// stateError logs a failed State call and notifies about persistent
// configuration problems at most once an hour each. Timers are untouched.
func (c *Checker) stateError(now time.Time, err error) {
	c.Log.Warn("scaleway state failed", "err", err)
	c.throttledNotice(now, err)
}

// throttledNotice reports whether err is an auth, not-found or key error
// and, if so, notifies about it at most once an hour per kind.
func (c *Checker) throttledNotice(now time.Time, err error) bool {
	var last *time.Time
	var msg string
	switch {
	case scw.IsAuth(err):
		last, msg = &c.lastAuthNotice, "Scaleway rejected the API key — automatic shutdown is disabled"
	case scw.IsNotFound(err):
		last, msg = &c.lastNotFound, "Scaleway can't find the instance — check the server ID; automatic shutdown is disabled"
	case errors.Is(err, scw.ErrKey):
		last, msg = &c.lastKeyNotice, "Can't read the Scaleway key from the Keychain — automatic shutdown is disabled"
	default:
		return false
	}
	c.mu.Lock()
	due := last.IsZero() || now.Sub(*last) >= noticeEvery
	if due {
		*last = now
	}
	c.mu.Unlock()
	if due {
		c.notify("steamway-activator", msg)
	}
	return true
}

// powerOff stops the instance and clears the timers on success.
//
// Residual race, accepted: the decision is taken from State and CurrentGame
// reads, and PowerOff is a separate call, so a Wake (MarkActive) landing during
// the PowerOff call still ends in a power off. Mirror case: this check reads
// "stopped in place" just as the waker sends poweron; the poweroff still wins.
// Pressing Wake again recovers: the waker sees "stopping", waits and starts the
// instance once it is stopped.
func (c *Checker) powerOff(ctx context.Context, res Result, reason, detail string) Result {
	if err := c.Instance.PowerOff(ctx); err != nil {
		c.Log.Error("power off failed", "reason", reason, "err", err)
		if !c.throttledNotice(c.Now(), err) {
			c.notify("steamway-activator", fmt.Sprintf("Couldn't power off the instance: %v", err))
		}
		return res
	}
	c.mu.Lock()
	c.disarm("powered off")
	c.mu.Unlock()
	c.Log.Info("powered off", "reason", reason)
	c.notify("steamway-activator", "Instance powered off: "+detail)
	res.PoweredOff, res.Reason = true, reason
	return res
}

func (c *Checker) notify(title, msg string) {
	if c.Notify != nil {
		c.Notify(title, msg)
	}
}
