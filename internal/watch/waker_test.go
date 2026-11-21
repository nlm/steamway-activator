package watch

import (
	"bytes"
	"context"
	"io"
	"log/slog"
	"net"
	"strings"
	"sync"
	"testing"
	"time"

	"steamway-activator/internal/scw"
)

var instMAC = net.HardwareAddr{0xde, 0x00, 0x00, 0xfe, 0x1d, 0x38}

func magic(mac net.HardwareAddr) []byte {
	p := bytes.Repeat([]byte{0xff}, 6)
	for range 16 {
		p = append(p, mac...)
	}
	return p
}

// scriptInstance returns states in order, repeating the last one.
type scriptInstance struct {
	mu         sync.Mutex
	states     []string
	stateErr   error
	errAt      map[int]error // State error by call index
	entered    chan struct{} // if set, PowerOn signals here then waits on release
	release    chan struct{}
	powerOnErr error
	stateCalls int
	powerOns   int
}

func (s *scriptInstance) State(context.Context) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	i := min(s.stateCalls, len(s.states)-1)
	err := s.stateErr
	if e, ok := s.errAt[s.stateCalls]; ok {
		err = e
	}
	s.stateCalls++
	return s.states[i], err
}

func (s *scriptInstance) PowerOn(context.Context) error {
	s.mu.Lock()
	s.powerOns++
	err, entered, release := s.powerOnErr, s.entered, s.release
	s.mu.Unlock()
	if entered != nil {
		entered <- struct{}{}
		<-release
	}
	return err
}

func (s *scriptInstance) PowerOff(context.Context) error { return nil }

func (s *scriptInstance) counts() (stateCalls, powerOns int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.stateCalls, s.powerOns
}

type wakeEnv struct {
	w      *Waker
	inst   *scriptInstance
	mu     sync.Mutex
	msgs   []string
	active int
	now    time.Time
}

func newWakeEnv(states ...string) *wakeEnv {
	e := &wakeEnv{inst: &scriptInstance{states: states}, now: t0}
	e.w = &Waker{
		MAC:      instMAC,
		Instance: e.inst,
		Notify: func(_, m string) {
			e.mu.Lock()
			e.msgs = append(e.msgs, m)
			e.mu.Unlock()
		},
		MarkActive: func() {
			e.mu.Lock()
			e.active++
			e.mu.Unlock()
		},
		Guard:          10 * time.Second,
		PollInterval:   time.Millisecond,
		PendingTimeout: time.Second, // tests that wait for the timeout shorten it
		Now: func() time.Time {
			e.mu.Lock()
			defer e.mu.Unlock()
			return e.now
		},
		Log: slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
	return e
}

func (e *wakeEnv) advance(d time.Duration) {
	e.mu.Lock()
	e.now = e.now.Add(d)
	e.mu.Unlock()
}

func (e *wakeEnv) handle() Outcome { return e.w.Handle(context.Background(), magic(instMAC)) }

func (e *wakeEnv) notes() []string {
	e.mu.Lock()
	defer e.mu.Unlock()
	return append([]string(nil), e.msgs...)
}

func (e *wakeEnv) activity() int {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.active
}

func (e *wakeEnv) wantNote(t *testing.T, sub string) {
	t.Helper()
	for _, m := range e.notes() {
		if strings.Contains(m, sub) {
			return
		}
	}
	t.Errorf("no notification containing %q in %q", sub, e.notes())
}

func (e *wakeEnv) wantNoNote(t *testing.T, sub string) {
	t.Helper()
	for _, m := range e.notes() {
		if strings.Contains(m, sub) {
			t.Errorf("unexpected notification %q", m)
		}
	}
}

func wantOutcome(t *testing.T, got, want Outcome) {
	t.Helper()
	if got != want {
		t.Fatalf("outcome %v, want %v", got, want)
	}
}

func TestOutcomeString(t *testing.T) {
	want := []string{"Ignored", "Guarded", "Starting", "Activity", "Pending", "Locked", "Started", "Failed"}
	for i, s := range want {
		if got := Outcome(i).String(); got != s {
			t.Errorf("Outcome(%d) = %q, want %q", i, got, s)
		}
	}
}

func TestWaker_IgnoresOtherMAC(t *testing.T) {
	e := newWakeEnv("stopped")
	other := net.HardwareAddr{0xaa, 0xbb, 0xcc, 0xdd, 0xee, 0xff}
	wantOutcome(t, e.w.Handle(context.Background(), magic(other)), Ignored)
	wantOutcome(t, e.w.Handle(context.Background(), []byte("junk")), Ignored)
	if s, p := e.inst.counts(); s != 0 || p != 0 {
		t.Errorf("State calls %d, PowerOn %d", s, p)
	}
}

func testStarts(t *testing.T, state string) {
	e := newWakeEnv(state)
	wantOutcome(t, e.handle(), Started)
	if _, p := e.inst.counts(); p != 1 {
		t.Errorf("PowerOn calls %d, want 1", p)
	}
}

func TestWaker_StartsFromStopped(t *testing.T)        { testStarts(t, "stopped") }
func TestWaker_StartsFromStoppedInPlace(t *testing.T) { testStarts(t, "stopped in place") }

func TestWaker_Guard(t *testing.T) {
	e := newWakeEnv("stopped")
	wantOutcome(t, e.handle(), Started)
	e.advance(time.Millisecond)
	wantOutcome(t, e.handle(), Guarded)
	if _, p := e.inst.counts(); p != 1 {
		t.Errorf("PowerOn calls %d, want 1", p)
	}
	e.advance(11 * time.Second)
	wantOutcome(t, e.handle(), Started)
	if _, p := e.inst.counts(); p != 2 {
		t.Errorf("PowerOn calls %d, want 2", p)
	}
}

func TestWaker_Starting(t *testing.T) {
	e := newWakeEnv("starting")
	wantOutcome(t, e.handle(), Starting)
	if _, p := e.inst.counts(); p != 0 || e.activity() != 0 {
		t.Errorf("PowerOn %d, MarkActive %d", p, e.activity())
	}
}

func TestWaker_RunningMarksActive(t *testing.T) {
	e := newWakeEnv("running")
	wantOutcome(t, e.handle(), Activity)
	if _, p := e.inst.counts(); p != 0 || e.activity() != 1 {
		t.Errorf("PowerOn %d, MarkActive %d", p, e.activity())
	}
}

func TestWaker_StoppingStartsWhenStopped(t *testing.T) {
	e := newWakeEnv("stopping", "stopping", "stopping", "stopped")
	wantOutcome(t, e.handle(), Pending)
	e.w.Wait()
	if _, p := e.inst.counts(); p != 1 {
		t.Errorf("PowerOn calls %d, want 1", p)
	}
	e.wantNote(t, "shutting down")
	e.wantNoNote(t, "didn't")
	// the start counts for the guard
	wantOutcome(t, e.handle(), Guarded)
}

func TestWaker_SinglePendingWait(t *testing.T) {
	e := newWakeEnv("stopping")
	e.w.PendingTimeout = 50 * time.Millisecond // this test waits for the timeout
	wantOutcome(t, e.handle(), Pending)
	wantOutcome(t, e.handle(), Pending)
	e.w.Wait()
	if _, p := e.inst.counts(); p != 0 {
		t.Errorf("PowerOn calls %d, want 0", p)
	}
	n := 0
	for _, m := range e.notes() {
		if strings.Contains(m, "didn't finish shutting down in 3 min") {
			n++
		}
		if strings.Contains(m, "will start again") {
			n += 100
		}
	}
	if n != 101 {
		t.Errorf("want one 'will start again' and one timeout notification, notes %q", e.notes())
	}
	// pending is cleared: a new packet starts a new wait
	wantOutcome(t, e.handle(), Pending)
	e.w.Wait()
}

func TestWaker_PendingStopsOnOtherState(t *testing.T) {
	e := newWakeEnv("stopping", "running")
	wantOutcome(t, e.handle(), Pending)
	e.w.Wait()
	if _, p := e.inst.counts(); p != 0 {
		t.Errorf("PowerOn calls %d, want 0", p)
	}
	e.wantNoNote(t, "didn't")
}

func TestWaker_Locked(t *testing.T) {
	e := newWakeEnv("locked")
	wantOutcome(t, e.handle(), Locked)
	e.wantNote(t, "locked")
	if _, p := e.inst.counts(); p != 0 {
		t.Errorf("PowerOn calls %d", p)
	}
}

func TestWaker_FailureNotifiesAndRetries(t *testing.T) {
	e := newWakeEnv("stopped")
	e.inst.powerOnErr = &scw.APIError{Status: 412, Message: "Not enough resources"}
	wantOutcome(t, e.handle(), Failed)
	e.wantNote(t, "Not enough resources")
	e.advance(time.Millisecond)
	wantOutcome(t, e.handle(), Failed)
	if _, p := e.inst.counts(); p != 2 {
		t.Errorf("PowerOn calls %d, want 2", p)
	}
}

func TestWaker_StateErrorNotifies(t *testing.T) {
	e := newWakeEnv("stopped")
	e.inst.stateErr = io.ErrUnexpectedEOF
	wantOutcome(t, e.handle(), Failed)
	e.wantNote(t, "Can't reach Scaleway to start the instance: unexpected EOF")
}

func TestWaker_PendingStartFailureNotifies(t *testing.T) {
	e := newWakeEnv("stopping", "stopped")
	e.inst.powerOnErr = &scw.APIError{Status: 412, Message: "Not enough resources"}
	wantOutcome(t, e.handle(), Pending)
	e.w.Wait()
	e.wantNote(t, "Instance didn't start: Not enough resources")
	// no guard after a failure: the next packet tries again (state is now "stopped")
	wantOutcome(t, e.handle(), Failed)
	if _, p := e.inst.counts(); p != 2 {
		t.Errorf("PowerOn calls %d, want 2", p)
	}
}

func TestWaker_ContextCancelEndsWait(t *testing.T) {
	e := newWakeEnv("stopping")
	ctx, cancel := context.WithCancel(context.Background())
	e.w.Context = ctx
	e.w.PendingTimeout = time.Hour
	wantOutcome(t, e.handle(), Pending)
	cancel()
	e.w.Wait()
	e.wantNoNote(t, "didn't")
}

func TestWaker_ErrorWhileWaitingKeepsPolling(t *testing.T) {
	e := newWakeEnv("stopping", "stopping", "stopped")
	e.inst.errAt = map[int]error{1: io.ErrUnexpectedEOF}
	wantOutcome(t, e.handle(), Pending)
	e.w.Wait()
	if _, p := e.inst.counts(); p != 1 {
		t.Errorf("PowerOn calls %d, want 1", p)
	}
	e.wantNoNote(t, "Can't reach")
}

func (e *wakeEnv) blockPowerOn() {
	e.inst.entered = make(chan struct{})
	e.inst.release = make(chan struct{})
}

func TestWaker_ConcurrentHandleSendsOnePowerOn(t *testing.T) {
	e := newWakeEnv("stopped")
	e.blockPowerOn()
	first := make(chan Outcome)
	go func() { first <- e.handle() }()
	<-e.inst.entered // first Handle is inside PowerOn
	wantOutcome(t, e.handle(), Guarded)
	e.inst.release <- struct{}{}
	wantOutcome(t, <-first, Started)
	if _, p := e.inst.counts(); p != 1 {
		t.Errorf("PowerOn calls %d, want 1", p)
	}
}

func TestWaker_HandleDuringPendingPowerOn(t *testing.T) {
	e := newWakeEnv("stopping", "stopped")
	e.blockPowerOn()
	wantOutcome(t, e.handle(), Pending)
	<-e.inst.entered // the wait goroutine is inside PowerOn
	wantOutcome(t, e.handle(), Guarded)
	e.inst.release <- struct{}{}
	e.w.Wait()
	if _, p := e.inst.counts(); p != 1 {
		t.Errorf("PowerOn calls %d, want 1", p)
	}
}

func TestWaker_FailedInflightLeavesNoGuard(t *testing.T) {
	e := newWakeEnv("stopped")
	e.inst.powerOnErr = io.ErrUnexpectedEOF
	wantOutcome(t, e.handle(), Failed)
	wantOutcome(t, e.handle(), Failed)
	if _, p := e.inst.counts(); p != 2 {
		t.Errorf("PowerOn calls %d, want 2", p)
	}
}

func (e *wakeEnv) count(sub string) int {
	n := 0
	for _, m := range e.notes() {
		if strings.Contains(m, sub) {
			n++
		}
	}
	return n
}

func TestWaker_StartNotifiesOnce(t *testing.T) {
	for _, state := range []string{"stopped", "stopped in place"} {
		e := newWakeEnv(state)
		wantOutcome(t, e.handle(), Started)
		if n := e.count("Starting the instance"); n != 1 {
			t.Errorf("%s: %d start notifications, want 1", state, n)
		}
	}
}

func TestWaker_GuardedNoStartNotice(t *testing.T) {
	e := newWakeEnv("stopped")
	wantOutcome(t, e.handle(), Started)
	e.advance(time.Millisecond)
	wantOutcome(t, e.handle(), Guarded)
	if n := e.count("Starting the instance"); n != 1 {
		t.Errorf("%d start notifications, want 1", n)
	}
}

func TestWaker_StartingAndActivityNoStartNotice(t *testing.T) {
	for _, state := range []string{"starting", "running"} {
		e := newWakeEnv(state)
		e.handle()
		if n := e.count("Starting the instance"); n != 0 {
			t.Errorf("%s: %d start notifications, want 0", state, n)
		}
	}
}

func TestWaker_FailedPowerOnNoStartNotice(t *testing.T) {
	e := newWakeEnv("stopped")
	e.inst.powerOnErr = io.ErrUnexpectedEOF
	wantOutcome(t, e.handle(), Failed)
	e.wantNote(t, "Instance didn't start")
	e.wantNoNote(t, "Starting the instance")
}

func TestWaker_PendingStartNotifiesStarting(t *testing.T) {
	e := newWakeEnv("stopping", "stopped")
	wantOutcome(t, e.handle(), Pending)
	e.w.Wait()
	e.wantNote(t, "will start again")
	if n := e.count("Starting the instance"); n != 1 {
		t.Errorf("%d start notifications, want 1", n)
	}
}
