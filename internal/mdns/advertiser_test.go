package mdns

import (
	"context"
	"fmt"
	"net"
	"testing"
	"time"

	"github.com/libp2p/zeroconf/v2"
)

func TestIdentityUsesLocalHostname(t *testing.T) {
	instance, hostName := identity()
	if instance == "" {
		t.Fatal("instance name must not be empty")
	}
	if len(hostName) < len(".local.") || hostName[len(hostName)-len(".local."):] != ".local." {
		t.Fatalf("host name %q must end in .local.", hostName)
	}
}

func TestAdvertiserIsDiscoverableOnLANInterface(t *testing.T) {
	const port = 32145
	_, hostName := identity()
	instance := fmt.Sprintf("Polemo mDNS Test %d", time.Now().UnixNano())
	server, _, err := register(instance, hostName, port, []string{
		"id=polemo-mdns-test",
		"name=Polemo Test Station",
		"path=/ws",
	})
	if err != nil {
		t.Skipf("no multicast LAN interface available: %v", err)
	}
	defer server.Shutdown()

	interfaces, err := multicastInterfaces()
	if err != nil {
		t.Fatal(err)
	}
	for _, networkInterface := range interfaces {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		entries := make(chan *zeroconf.ServiceEntry, 16)
		go func() {
			_ = zeroconf.Browse(
				ctx,
				service,
				"local.",
				entries,
				zeroconf.SelectIfaces([]net.Interface{*networkInterface}),
				zeroconf.SelectIPTraffic(zeroconf.IPv4),
			)
		}()
		for {
			select {
			case entry := <-entries:
				if entry != nil && entry.Port == port {
					cancel()
					return
				}
			case <-ctx.Done():
				cancel()
				goto nextInterface
			}
		}
	nextInterface:
	}
	t.Fatal("mDNS service was not discoverable on an active LAN interface")
}

func TestAdvertiserHealthCheckSurvivesRegistrationRestart(t *testing.T) {
	const port = 32146
	_, hostName := identity()
	instance := fmt.Sprintf("Polemo mDNS Recovery Test %d", time.Now().UnixNano())
	txt := []string{"id=polemo-mdns-recovery-test", "path=/ws"}
	server, interfaces, err := register(instance, hostName, port, txt)
	if err != nil {
		t.Skipf("no multicast LAN interface available: %v", err)
	}
	advertiser := &Advertiser{
		server:     server,
		interfaces: interfaces,
		instance:   instance,
		hostName:   hostName,
		port:       port,
		txt:        txt,
		stopCh:     make(chan struct{}),
	}
	defer advertiser.Stop()

	if !advertiser.healthy() {
		t.Fatal("new mDNS registration did not pass its health check")
	}
	advertiser.restart()
	if !advertiser.healthy() {
		t.Fatal("mDNS registration did not recover after restart")
	}
}
