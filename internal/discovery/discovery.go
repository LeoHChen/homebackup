// Package discovery advertises the server on the local network with Bonjour
// (DNS-SD service type _homebackup._tcp), so the apps find it without an IP.
//
// It uses the operating system's own responder: `dns-sd` on macOS and
// `avahi-publish-service` on Linux. If neither is available, it logs and
// does nothing; apps can still connect by host name.
package discovery

import (
	"context"
	"log/slog"
	"os/exec"
	"runtime"
	"strconv"
	"time"
)

// ServiceType is the DNS-SD service type.
const ServiceType = "_homebackup._tcp"

// Command returns the command that advertises the service, or nil if the
// platform has no supported responder.
func Command(ctx context.Context, name string, port int, txt []string) *exec.Cmd {
	p := strconv.Itoa(port)
	switch runtime.GOOS {
	case "darwin":
		args := append([]string{"-R", name, ServiceType, "local", p}, txt...)
		return exec.CommandContext(ctx, "dns-sd", args...)
	case "linux":
		if _, err := exec.LookPath("avahi-publish-service"); err != nil {
			return nil
		}
		args := append([]string{name, ServiceType, p}, txt...)
		return exec.CommandContext(ctx, "avahi-publish-service", args...)
	}
	return nil
}

// Advertise keeps the service registered until ctx is cancelled, restarting
// the responder if it exits.
func Advertise(ctx context.Context, name string, port int, txt []string, log *slog.Logger) {
	for {
		cmd := Command(ctx, name, port, txt)
		if cmd == nil {
			log.Info("bonjour not available on this system; skipping advertisement")
			return
		}
		log.Info("advertising on the local network", "service", ServiceType, "name", name, "port", port)
		err := cmd.Run()
		if ctx.Err() != nil {
			return
		}
		log.Warn("bonjour advertiser exited; restarting", "err", err)
		select {
		case <-ctx.Done():
			return
		case <-time.After(30 * time.Second):
		}
	}
}
