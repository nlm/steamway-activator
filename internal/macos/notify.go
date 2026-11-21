package macos

import "context"

// Notify shows a macOS notification. Title and message are passed as script
// arguments, never interpolated, so they cannot break or inject AppleScript.
func Notify(ctx context.Context, run Runner, title, message string) error {
	_, err := run(ctx, "osascript",
		"-e", "on run argv",
		"-e", "display notification (item 2 of argv) with title (item 1 of argv)",
		"-e", "end run",
		title, message)
	return err
}
