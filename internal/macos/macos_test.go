package macos

import (
	"context"
	"errors"
	"slices"
	"testing"
)

type call struct {
	name string
	args []string
}

func fakeRunner(out string, err error, calls *[]call) Runner {
	return func(_ context.Context, name string, args ...string) ([]byte, error) {
		*calls = append(*calls, call{name, args})
		return []byte(out), err
	}
}

func TestReadSecret(t *testing.T) {
	var calls []call
	got, err := ReadSecret(t.Context(), fakeRunner("s3cr3t\n", nil, &calls), "steamway-activator", "scw-secret-key")
	if err != nil {
		t.Fatal(err)
	}
	if got != "s3cr3t" {
		t.Errorf("got %q", got)
	}
	want := []string{"find-generic-password", "-s", "steamway-activator", "-a", "scw-secret-key", "-w"}
	if len(calls) != 1 || calls[0].name != "security" || !slices.Equal(calls[0].args, want) {
		t.Errorf("calls = %+v", calls)
	}
}

func TestReadSecret_Errors(t *testing.T) {
	var calls []call
	if _, err := ReadSecret(t.Context(), fakeRunner("", errors.New("boom"), &calls), "s", "a"); err == nil {
		t.Error("expected error on runner failure")
	}
	if _, err := ReadSecret(t.Context(), fakeRunner("\n", nil, &calls), "s", "a"); err == nil {
		t.Error("expected error on empty secret")
	}
}

func TestNotify_ArgvNotInterpolated(t *testing.T) {
	var calls []call
	msg := `Instance didn't start: "Not enough resources"`
	if err := Notify(t.Context(), fakeRunner("", nil, &calls), "steamway-activator", msg); err != nil {
		t.Fatal(err)
	}
	want := []string{
		"-e", "on run argv",
		"-e", "display notification (item 2 of argv) with title (item 1 of argv)",
		"-e", "end run",
		"steamway-activator", msg,
	}
	if len(calls) != 1 || calls[0].name != "osascript" || !slices.Equal(calls[0].args, want) {
		t.Errorf("calls = %+v", calls)
	}
}
