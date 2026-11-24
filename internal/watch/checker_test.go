package watch

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"steamway-activator/internal/scw"
)

var t0 = time.Date(2026, 11, 21, 20, 0, 0, 0, time.UTC)

type fakeInstance struct {
	state       string
	stateErr    error
	powerOffErr error
	powerOffs   int
}

func (f *fakeInstance) State(context.Context) (string, error) { return f.state, f.stateErr }
func (f *fakeInstance) PowerOn(context.Context) error         { return nil }
func (f *fakeInstance) PowerOff(context.Context) error {
	f.powerOffs++
	return f.powerOffErr
}

type fakeSunshine struct {
	game  int
	err   error
	calls int
}

func (f *fakeSunshine) CurrentGame(context.Context) (int, error) {
	f.calls++
	return f.game, f.err
}

type env struct {
	t     *testing.T
	inst  *fakeInstance
	sun   *fakeSunshine
	c     *Checker
	now   time.Time
	notes []string
}

func newEnv(t *testing.T) *env {
	e := &env{t: t, inst: &fakeInstance{state: "running"}, sun: &fakeSunshine{}, now: t0}
	e.c = &Checker{
		Instance:         e.inst,
		Sunshine:         e.sun,
		Notify:           func(title, msg string) { e.notes = append(e.notes, title+": "+msg) },
		IdleLimit:        5 * time.Minute,
		UnreachableLimit: 10 * time.Minute,
		Interval:         time.Minute,
		Now:              func() time.Time { return e.now },
		Log:              slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
	e.c.Arm() // most tests watch a session this program started
	e.notes = nil
	return e
}

// newIdleEnv is newEnv without Arm: an instance this program didn't start.
func newIdleEnv(t *testing.T) *env {
	e := newEnv(t)
	e.c = &Checker{
		Instance: e.inst, Sunshine: e.sun, Notify: e.c.Notify,
		IdleLimit: e.c.IdleLimit, UnreachableLimit: e.c.UnreachableLimit, Interval: e.c.Interval,
		Now: e.c.Now, Log: e.c.Log,
	}
	return e
}

// at moves the clock to t0+d and runs one check with the given scripted answers.
func (e *env) at(d time.Duration, state string, game int, sunErr error) Result {
	e.t.Helper()
	e.now = t0.Add(d)
	e.inst.state, e.inst.stateErr = state, nil
	e.sun.game, e.sun.err = game, sunErr
	return e.c.Check(context.Background())
}

func (e *env) atStateErr(d time.Duration, err error) Result {
	e.t.Helper()
	e.now = t0.Add(d)
	e.inst.stateErr = err
	return e.c.Check(context.Background())
}

func (e *env) wantPowerOffs(n int) {
	e.t.Helper()
	if e.inst.powerOffs != n {
		e.t.Fatalf("PowerOff calls = %d, want %d", e.inst.powerOffs, n)
	}
}

var errNoAnswer = errors.New("no answer")

const mn = time.Minute

func TestCheck_StoppedDoesNothing(t *testing.T) {
	e := newEnv(t)
	for i := range 3 { // within the start grace
		r := e.at(time.Duration(i)*mn, "stopped", 0, nil)
		if r.PoweredOff || r.Reason != "" || r.State != "stopped" {
			t.Fatalf("result %+v", r)
		}
	}
	if e.sun.calls != 0 {
		t.Fatalf("Sunshine called %d times", e.sun.calls)
	}
	e.wantPowerOffs(0)
}

func TestCheck_StoppedInPlacePowersOff(t *testing.T) {
	e := newEnv(t)
	r := e.at(0, "stopped in place", 0, nil)
	if !r.PoweredOff || r.Reason != "stopped in place" {
		t.Fatalf("result %+v", r)
	}
	e.wantPowerOffs(1)
	if e.sun.calls != 0 {
		t.Fatal("Sunshine must not be called")
	}
	if len(e.notes) != 1 || !strings.Contains(e.notes[0], "Instance powered off: it was stopped in place") {
		t.Fatalf("notes %q", e.notes)
	}
}

func TestCheck_IdleClockStartsAtFirstAnswer(t *testing.T) {
	e := newEnv(t)
	e.at(0, "running", 0, errNoAnswer) // boot
	for i := 1; i <= 5; i++ {
		r := e.at(time.Duration(i)*mn, "running", 0, nil)
		if r.PoweredOff {
			t.Fatalf("powered off too early at %dm", i)
		}
	}
	r := e.at(6*mn, "running", 0, nil)
	if !r.PoweredOff || r.Reason != "idle" {
		t.Fatalf("result %+v", r)
	}
	e.wantPowerOffs(1)
	notes := e.otherNotes()
	if len(notes) != 1 || !strings.Contains(notes[0], "Instance powered off: no app running for 5m0s") {
		t.Fatalf("notes %q", notes)
	}
}

func TestCheck_GameKeepsAlive(t *testing.T) {
	e := newEnv(t)
	for i := 0; i <= 30; i++ {
		if r := e.at(time.Duration(i)*mn, "running", 7, nil); r.PoweredOff {
			t.Fatal("powered off during a game")
		}
	}
	for i := 31; i < 35; i++ {
		if r := e.at(time.Duration(i)*mn, "running", 0, nil); r.PoweredOff {
			t.Fatalf("early power off at %dm", i)
		}
	}
	r := e.at(35*mn, "running", 0, nil)
	if !r.PoweredOff || r.Reason != "idle" {
		t.Fatalf("result %+v", r)
	}
	e.wantPowerOffs(1)
}

func TestCheck_UnreachablePowersOff(t *testing.T) {
	e := newEnv(t)
	for i := 0; i <= 9; i++ {
		if r := e.at(time.Duration(i)*mn, "running", 0, errNoAnswer); r.PoweredOff {
			t.Fatalf("early power off at %dm", i)
		}
	}
	r := e.at(10*mn, "running", 0, errNoAnswer)
	if !r.PoweredOff || r.Reason != "unreachable" {
		t.Fatalf("result %+v", r)
	}
	if len(e.notes) != 1 || !strings.Contains(e.notes[0], "Sunshine not answering for 10m0s") {
		t.Fatalf("notes %q", e.notes)
	}
}

func TestCheck_AnswerResetsUnreachable(t *testing.T) {
	e := newEnv(t)
	for i := 0; i <= 5; i++ {
		e.at(time.Duration(i)*mn, "running", 0, errNoAnswer)
	}
	e.at(6*mn, "running", 3, nil)
	for i := 7; i <= 16; i++ {
		if r := e.at(time.Duration(i)*mn, "running", 0, errNoAnswer); r.PoweredOff {
			t.Fatalf("power off at %dm", i)
		}
	}
	r := e.at(17*mn, "running", 0, errNoAnswer)
	if !r.PoweredOff || r.Reason != "unreachable" {
		t.Fatalf("result %+v", r)
	}
}

func TestCheck_ScalewayDownDoesNothing(t *testing.T) {
	e := newEnv(t)
	e.at(0, "running", 0, nil)
	calls := e.sun.calls
	for i := 1; i <= 180; i++ {
		r := e.atStateErr(time.Duration(i)*mn, errors.New("dial tcp: no route"))
		if r.PoweredOff {
			t.Fatal("powered off with Scaleway down")
		}
	}
	if e.sun.calls != calls {
		t.Fatal("Sunshine called while Scaleway down")
	}
	e.wantPowerOffs(0)
	notes := e.otherNotes()
	if len(notes) != 0 {
		t.Fatalf("notes %q", notes)
	}
	r := e.at(181*mn, "running", 0, nil)
	if !r.PoweredOff || r.Reason != "idle" {
		t.Fatalf("result %+v", r)
	}
}

func TestCheck_GapCountsAsIdle(t *testing.T) {
	e := newEnv(t)
	e.at(0, "running", 0, nil)
	r := e.at(8*time.Hour, "running", 0, nil)
	if !r.PoweredOff || r.Reason != "idle" {
		t.Fatalf("result %+v", r)
	}
}

func TestCheck_GapClearsUnreachable(t *testing.T) {
	e := newEnv(t)
	for i := 0; i <= 8; i++ {
		e.at(time.Duration(i)*mn, "running", 0, errNoAnswer)
	}
	gap := 8 * time.Hour
	if r := e.at(gap, "running", 0, errNoAnswer); r.PoweredOff {
		t.Fatal("powered off right after a gap")
	}
	for i := 1; i <= 9; i++ {
		if r := e.at(gap+time.Duration(i)*mn, "running", 0, errNoAnswer); r.PoweredOff {
			t.Fatalf("power off %dm after gap", i)
		}
	}
	r := e.at(gap+10*mn, "running", 0, errNoAnswer)
	if !r.PoweredOff || r.Reason != "unreachable" {
		t.Fatalf("result %+v", r)
	}
}

func hourlyNotice(t *testing.T, err error, want string) {
	t.Helper()
	e := newEnv(t)
	for i := range 120 {
		d := time.Duration(i) * mn
		if r := e.atStateErr(d, err); r.PoweredOff {
			t.Fatal("powered off")
		}
	}
	if len(e.notes) != 2 {
		t.Fatalf("got %d notifications: %q", len(e.notes), e.notes)
	}
	for _, n := range e.notes {
		if !strings.Contains(n, want) {
			t.Fatalf("notification %q lacks %q", n, want)
		}
	}
	e.wantPowerOffs(0)
}

func TestCheck_AuthErrorNotifiesHourly(t *testing.T) {
	hourlyNotice(t, &scw.APIError{Status: 401}, "Scaleway rejected the API key — automatic shutdown is disabled")
}

func TestCheck_NotFoundNotifiesHourly(t *testing.T) {
	hourlyNotice(t, &scw.APIError{Status: 404, Message: "resource is not found"},
		"Scaleway can't find the instance — check the server ID; automatic shutdown is disabled")
}

func TestCheck_KeyErrorNotifiesHourly(t *testing.T) {
	hourlyNotice(t, fmt.Errorf("%w: locked", scw.ErrKey),
		"Can't read the Scaleway key from the Keychain — automatic shutdown is disabled")
}

func TestCheck_PowerOffFailureRetries(t *testing.T) {
	e := newEnv(t)
	e.at(0, "running", 0, nil)
	e.inst.powerOffErr = errors.New("boom")
	r := e.at(5*mn, "running", 0, nil)
	if r.PoweredOff {
		t.Fatal("PoweredOff despite failure")
	}
	notes := e.otherNotes()
	if len(notes) != 1 || !strings.Contains(notes[0], "Couldn't power off the instance: boom") {
		t.Fatalf("notes %q", notes)
	}
	e.inst.powerOffErr = nil
	r = e.at(6*mn, "running", 0, nil)
	if !r.PoweredOff || r.Reason != "idle" {
		t.Fatalf("result %+v", r)
	}
	e.wantPowerOffs(2)
}

func TestCheck_MarkActiveDelaysIdle(t *testing.T) {
	e := newEnv(t)
	e.at(0, "running", 0, nil)
	e.now = t0.Add(4 * mn)
	e.c.MarkActive()
	for _, d := range []time.Duration{5 * mn, 8 * mn} {
		if r := e.at(d, "running", 0, nil); r.PoweredOff {
			t.Fatalf("power off at %v", d)
		}
	}
	if r := e.at(9*mn, "running", 0, nil); !r.PoweredOff || r.Reason != "idle" {
		t.Fatalf("result %+v", r)
	}
}

func TestCheck_MarkActiveConcurrent(t *testing.T) {
	e := newEnv(t)
	var wg sync.WaitGroup
	for range 100 {
		wg.Go(e.c.MarkActive)
	}
	for range 100 {
		e.c.Check(context.Background())
	}
	wg.Wait()
}

func TestCheck_NotRunningClearsTimers(t *testing.T) {
	e := newEnv(t)
	e.at(0, "running", 0, nil)
	e.at(1*mn, "stopping", 0, nil)
	if r := e.at(10*mn, "running", 0, nil); r.PoweredOff {
		t.Fatal("powered off; idle should restart at 10m")
	}
	e.wantPowerOffs(0)
}

func TestCheck_PowerOffAuthFailureNotifiesHourly(t *testing.T) {
	e := newEnv(t)
	e.inst.powerOffErr = &scw.APIError{Status: 403}
	for i := range 65 {
		if r := e.at(time.Duration(i)*mn, "running", 0, nil); r.PoweredOff {
			t.Fatal("PoweredOff despite failure")
		}
	}
	e.wantPowerOffs(60) // retried at every check from 5m on
	notes := e.otherNotes()
	if len(notes) != 1 || !strings.Contains(notes[0], "Scaleway rejected the API key") {
		t.Fatalf("notes %q", notes)
	}
}

func TestCheck_PowerOffSuccessClearsTimers(t *testing.T) {
	e := newEnv(t)
	e.at(0, "running", 0, nil)
	if r := e.at(6*mn, "running", 0, nil); !r.PoweredOff {
		t.Fatalf("result %+v, want idle power off", r)
	}
	e.wantPowerOffs(1)
	// Still reported running with no game: the idle clock restarts, no second PowerOff.
	if r := e.at(7*mn, "running", 0, nil); r.PoweredOff {
		t.Fatalf("second power off right after the first: %+v", r)
	}
	e.wantPowerOffs(1)
}

// otherNotes is e.notes without the "Instance ready" notices.
func (e *env) otherNotes() []string {
	var out []string
	for _, m := range e.notes {
		if !strings.Contains(m, "Instance ready: open Moonlight") {
			out = append(out, m)
		}
	}
	return out
}

func (e *env) readyNotes() int {
	n := 0
	for _, m := range e.notes {
		if strings.Contains(m, "Instance ready: open Moonlight") {
			n++
		}
	}
	return n
}

func (e *env) wantReady(n int) {
	e.t.Helper()
	if got := e.readyNotes(); got != n {
		e.t.Errorf("ready notifications %d, want %d (%q)", got, n, e.notes)
	}
}

func TestCheck_ReadyNotifiesAfterBoot(t *testing.T) {
	e := newEnv(t)
	e.at(0, "stopped", 0, nil)
	e.at(time.Minute, "running", 0, errors.New("refused"))
	e.wantReady(0)
	e.at(2*time.Minute, "running", 0, nil)
	e.wantReady(1)
	e.at(3*time.Minute, "running", 0, nil)
	e.at(4*time.Minute, "running", 1, nil)
	e.wantReady(1)
}

func TestCheck_ReadyOnceAfterArm(t *testing.T) {
	e := newEnv(t)
	e.at(0, "running", 0, nil)
	e.at(time.Minute, "running", 1, nil)
	e.wantReady(1)
}

func TestCheck_ReadyEachNonRunningState(t *testing.T) {
	for _, st := range []string{"stopped", "starting", "stopping", "locked"} {
		e := newEnv(t)
		e.at(0, st, 0, nil)
		e.at(time.Minute, "running", 0, nil)
		e.wantReady(1)
	}
}

func TestCheck_ReadyScalewayErrorKeepsState(t *testing.T) {
	e := newEnv(t)
	e.at(0, "starting", 0, nil)
	e.atStateErr(time.Minute, errors.New("offline"))
	e.wantReady(0)
	e.at(2*time.Minute, "running", 0, nil)
	e.wantReady(1)
}

func TestCheck_ReadySleepGapKeepsState(t *testing.T) {
	e := newEnv(t)
	e.at(0, "stopped", 0, nil)
	e.at(time.Hour, "running", 0, nil)
	e.wantReady(1)
}

func TestCheck_NotArmedMakesNoCall(t *testing.T) {
	e := newIdleEnv(t)
	for i, st := range []string{"running", "stopped in place", "running"} {
		if r := e.at(time.Duration(i)*time.Hour, st, 0, nil); r != (Result{}) {
			t.Fatalf("result %+v", r)
		}
	}
	if e.sun.calls != 0 || len(e.notes) != 0 {
		t.Fatalf("Sunshine calls %d, notes %q", e.sun.calls, e.notes)
	}
	e.wantPowerOffs(0)
}

func TestCheck_MarkActiveStartsWatching(t *testing.T) {
	e := newIdleEnv(t)
	e.now = t0
	e.c.MarkActive() // Wake PC on an instance started from the console
	if !e.c.Armed() {
		t.Fatal("not watching after MarkActive")
	}
	e.at(mn, "running", 0, nil)
	e.wantReady(1)
	if r := e.at(4*mn, "running", 0, nil); r.PoweredOff {
		t.Fatal("idle clock must start at the wake")
	}
	if r := e.at(5*mn, "running", 0, nil); !r.PoweredOff || r.Reason != "idle" {
		t.Fatalf("result %+v", r)
	}
}

func TestCheck_MarkActiveWhileWatchingNoReady(t *testing.T) {
	e := newEnv(t)
	e.at(0, "running", 0, nil)
	e.wantReady(1)
	e.now = t0.Add(mn)
	e.c.MarkActive()
	e.at(2*mn, "running", 0, nil)
	e.wantReady(1)
}

func TestCheck_PowerOffDisarms(t *testing.T) {
	e := newEnv(t)
	e.at(0, "running", 0, nil)
	if r := e.at(6*mn, "running", 0, nil); !r.PoweredOff {
		t.Fatalf("result %+v", r)
	}
	if e.c.Armed() {
		t.Fatal("still armed after power off")
	}
	calls := e.sun.calls
	e.at(time.Hour, "running", 0, nil) // restarted from the console
	if e.sun.calls != calls {
		t.Fatal("still watching")
	}
	e.wantPowerOffs(1)
}

func TestCheck_StoppedAfterUpDisarms(t *testing.T) {
	for _, st := range []string{"stopped", "stopping"} {
		e := newEnv(t)
		e.at(0, "running", 0, nil)
		e.at(mn, st, 0, nil) // stopped from the console
		if e.c.Armed() {
			t.Fatalf("%s: still armed", st)
		}
	}
}

func TestCheck_StoppedRightAfterArmKeepsWatching(t *testing.T) {
	e := newEnv(t)
	e.at(0, "stopped", 0, nil) // Scaleway lag after poweron
	e.at(mn, "stopped", 0, nil)
	if !e.c.Armed() {
		t.Fatal("disarmed within the start grace")
	}
	e.at(2*mn, "running", 0, nil)
	if r := e.at(8*mn, "running", 0, nil); !r.PoweredOff {
		t.Fatalf("result %+v, want idle power off", r)
	}
}

func TestCheck_NeverUpDisarmsAfterGrace(t *testing.T) {
	e := newEnv(t)
	for i := 0; i <= 3; i++ {
		e.at(time.Duration(i)*mn, "stopped", 0, nil)
	}
	if e.c.Armed() {
		t.Fatal("still armed after the start grace")
	}
}
