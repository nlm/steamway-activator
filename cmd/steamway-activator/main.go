// Command steamway-activator starts the instance on Moonlight's Wake-on-LAN packet
// and powers it off when Sunshine reports no running app.
package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"net"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"steamway-activator/internal/macos"
	"steamway-activator/internal/scw"
	"steamway-activator/internal/sunshine"
	"steamway-activator/internal/watch"
)

const (
	notifyTitle   = "steamway-activator"
	handleTimeout = 30 * time.Second
	checkTimeout  = 30 * time.Second
)

func main() {
	os.Exit(run())
}

func run() int {
	var (
		zone, serverID, host, macStr, listen  string
		wakeFrom                              string
		service, account, apiURL              string
		idleLimit, unreachableLimit, interval time.Duration
	)
	fs := flag.NewFlagSet("steamway-activator", flag.ContinueOnError)
	fs.StringVar(&zone, "zone", "", "Scaleway zone, e.g. fr-par-2 (required)")
	fs.StringVar(&serverID, "server-id", "", "Scaleway instance server ID (required)")
	fs.StringVar(&host, "host", "", "Instance address, for Sunshine's serverinfo (required)")
	fs.StringVar(&macStr, "mac", "", "MAC address of the instance to wake (required)")
	fs.DurationVar(&idleLimit, "idle-limit", 5*time.Minute, "power off after this long with no app running")
	fs.DurationVar(&unreachableLimit, "unreachable-limit", 10*time.Minute, "power off after Sunshine is unreachable this long")
	fs.DurationVar(&interval, "interval", 60*time.Second, "check interval")
	fs.StringVar(&listen, "listen", "0.0.0.0:9", "UDP address to listen on for magic packets")
	fs.StringVar(&wakeFrom, "wake-from", "", "extra Wake PC sources besides this machine: comma-separated IPs or CIDRs, or \"any\"")
	fs.StringVar(&service, "keychain-service", "steamway-activator", "Keychain service holding the Scaleway secret key")
	fs.StringVar(&account, "keychain-account", "scw-secret-key", "Keychain account holding the Scaleway secret key")
	fs.StringVar(&apiURL, "api-url", "https://api.scaleway.com", "Scaleway API URL (override for tests only)")
	if err := fs.Parse(os.Args[1:]); err != nil {
		return 2
	}
	usage := func(msg string) int {
		fmt.Fprintln(os.Stderr, "steamway-activator:", msg)
		fs.Usage()
		return 2
	}
	for name, v := range map[string]string{"--zone": zone, "--server-id": serverID, "--host": host, "--mac": macStr} {
		if v == "" {
			return usage(name + " is required")
		}
	}
	mac, err := net.ParseMAC(macStr)
	if err != nil {
		return usage("invalid --mac: " + err.Error())
	}
	extraSource, err := parseWakeFrom(wakeFrom)
	if err != nil {
		return usage(err.Error())
	}
	if idleLimit <= 0 || unreachableLimit <= 0 || interval <= 0 {
		return usage("--idle-limit, --unreachable-limit and --interval must be positive")
	}

	log := slog.New(slog.NewTextHandler(os.Stderr, nil))
	now := func() time.Time { return time.Now().Round(0) }

	key := func(ctx context.Context) (string, error) {
		return macos.ReadSecret(ctx, macos.Exec, service, account)
	}
	inst, err := scw.New(apiURL, zone, serverID, key)
	if err != nil {
		return usage(err.Error())
	}
	sun := &sunshine.Client{BaseURL: "http://" + net.JoinHostPort(host, "47989")}
	notify := func(title, message string) {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := macos.Notify(ctx, macos.Exec, title, message); err != nil {
			log.Warn("notification failed", "err", err, "message", message)
		}
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	checker := &watch.Checker{
		Instance: inst, Sunshine: sun, Notify: notify,
		IdleLimit: idleLimit, UnreachableLimit: unreachableLimit, Interval: interval,
		Now: now, Log: log,
	}
	waker := &watch.Waker{
		MAC: mac, Instance: inst, Notify: notify, MarkActive: checker.MarkActive, Started: checker.Arm,
		Guard: 10 * time.Second, PollInterval: 10 * time.Second, PendingTimeout: 3 * time.Minute,
		Now: now, Log: log, Context: ctx,
	}

	acceptSource := func(ip net.IP) bool { return isLocalIP(ip) || extraSource(ip) }
	wakeSources := "local only"
	if strings.TrimSpace(wakeFrom) != "" {
		wakeSources = "local and " + wakeFrom
	}
	log.Info("started", "wake_sources", wakeSources, "listen", listen, "zone", zone, "server", serverID, "mac", mac.String())

	listener := &listenSupervisor{
		Listen: func() (net.PacketConn, error) { return net.ListenPacket("udp4", listen) },
		Serve: func(ctx context.Context, pc net.PacketConn) error {
			return readLoop(ctx, pc, func(pkt []byte) {
				hctx, cancel := context.WithTimeout(ctx, handleTimeout)
				defer cancel()
				waker.Handle(hctx, pkt)
			}, acceptSource, log, readBackoff, readMaxFailures)
		},
		Notify: func(msg string) { notify(notifyTitle, msg) },
		Retry:  listenRetry,
		Now:    now,
		Log:    log,
		Bound:  func(pc net.PacketConn) { log.Info("listening", "addr", pc.LocalAddr().String()) },
	}
	listenerDone := make(chan struct{})
	go func() {
		defer close(listenerDone)
		listener.Run(ctx)
	}()

	check := func() {
		cctx, cancel := context.WithTimeout(ctx, checkTimeout)
		defer cancel()
		checker.Check(cctx)
	}
	check()
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
loop:
	for {
		select {
		case <-ctx.Done():
			break loop
		case <-ticker.C:
			check()
		}
	}

	log.Info("shutting down")
	stop()
	<-listenerDone // closes the socket first
	waker.Wait()
	return 0
}
