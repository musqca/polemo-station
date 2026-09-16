package mdns

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net"
	"os"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/libp2p/zeroconf/v2"
)

const service = "_polemo._tcp"

// Advertiser owns the lifetime of the Polemo Station Bonjour service.
type Advertiser struct {
	mu         sync.RWMutex
	server     *zeroconf.Server
	interfaces []net.Interface
	instance   string
	hostName   string
	port       int
	txt        []string
	stopOnce   sync.Once
	stopped    atomic.Bool
	stopCh     chan struct{}
}

// Start publishes the station's TCP port and basic connection metadata.
func Start(port int, values map[string]string) (*Advertiser, error) {
	if port < 1 || port > 65535 {
		return nil, fmt.Errorf("invalid mDNS service port: %d", port)
	}

	instance, hostName := identity()
	txt := make([]string, 0, len(values))
	keys := make([]string, 0, len(values))
	for key := range values {
		if key != "" {
			keys = append(keys, key)
		}
	}
	sort.Strings(keys)
	for _, key := range keys {
		txt = append(txt, key+"="+values[key])
	}

	server, selectedInterfaces, err := register(
		instance,
		hostName,
		port,
		txt,
	)
	if err != nil {
		return nil, err
	}
	advertiser := &Advertiser{
		server:     server,
		interfaces: selectedInterfaces,
		instance:   instance,
		hostName:   hostName,
		port:       port,
		txt:        append([]string(nil), txt...),
		stopCh:     make(chan struct{}),
	}
	go advertiser.monitor()
	return advertiser, nil
}

func (a *Advertiser) Interfaces() []string {
	if a == nil {
		return nil
	}
	a.mu.RLock()
	defer a.mu.RUnlock()
	names := make([]string, 0, len(a.interfaces))
	for _, networkInterface := range a.interfaces {
		names = append(names, networkInterface.Name)
	}
	return names
}

func (a *Advertiser) Stop() {
	if a == nil {
		return
	}
	a.stopOnce.Do(func() {
		a.stopped.Store(true)
		close(a.stopCh)
		a.mu.Lock()
		defer a.mu.Unlock()
		if a.server != nil {
			a.server.Shutdown()
			a.server = nil
		}
	})
}

func register(
	instance string,
	hostName string,
	port int,
	txt []string,
) (*zeroconf.Server, []net.Interface, error) {
	interfaces, err := multicastInterfaces()
	if err != nil {
		return nil, nil, err
	}
	selectedInterfaces := make([]net.Interface, 0, len(interfaces))
	ips := make([]string, 0, len(interfaces))
	for _, networkInterface := range interfaces {
		addresses, addressErr := interfaceAddresses(networkInterface)
		if addressErr != nil {
			continue
		}
		selectedInterfaces = append(selectedInterfaces, *networkInterface)
		for _, address := range addresses {
			ips = append(ips, address.String())
		}
	}
	if len(selectedInterfaces) == 0 {
		return nil, nil, errors.New("no multicast LAN interface with a usable address")
	}
	server, err := zeroconf.RegisterProxy(
		instance,
		service,
		"local",
		port,
		hostName,
		ips,
		txt,
		selectedInterfaces,
	)
	if err != nil {
		return nil, nil, fmt.Errorf("register mDNS service: %w", err)
	}
	return server, selectedInterfaces, nil
}

func (a *Advertiser) monitor() {
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			if !a.healthy() {
				a.restart()
			}
		case <-a.stopCh:
			return
		}
	}
}

func (a *Advertiser) healthy() bool {
	a.mu.RLock()
	interfaces := append([]net.Interface(nil), a.interfaces...)
	port := a.port
	a.mu.RUnlock()
	if len(interfaces) == 0 || a.stopped.Load() {
		return false
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2500*time.Millisecond)
	defer cancel()
	entries := make(chan *zeroconf.ServiceEntry, 8)
	errCh := make(chan error, 1)
	go func() {
		errCh <- zeroconf.Browse(
			ctx,
			service,
			"local.",
			entries,
			zeroconf.SelectIfaces(interfaces),
			zeroconf.SelectIPTraffic(zeroconf.IPv4),
		)
	}()

	for {
		select {
		case entry, ok := <-entries:
			if !ok {
				return false
			}
			if entry != nil && entry.Port == port {
				return true
			}
		case <-ctx.Done():
			return false
		case <-errCh:
			return false
		}
	}
}

func (a *Advertiser) restart() {
	if a.stopped.Load() {
		return
	}
	server, interfaces, err := register(a.instance, a.hostName, a.port, a.txt)
	if err != nil {
		log.Printf("mDNS recovery failed: %v", err)
		return
	}

	a.mu.Lock()
	if a.stopped.Load() {
		a.mu.Unlock()
		server.Shutdown()
		return
	}
	oldServer := a.server
	a.server = server
	a.interfaces = interfaces
	a.mu.Unlock()
	if oldServer != nil {
		oldServer.Shutdown()
	}
	log.Printf("mDNS discovery recovered on %s", strings.Join(a.Interfaces(), ", "))
}

func identity() (instance string, hostName string) {
	name, err := os.Hostname()
	if err != nil || strings.TrimSpace(name) == "" {
		name = "polemo-station"
	}
	name = strings.TrimSuffix(strings.TrimSuffix(name, "."), ".local")
	return "Polemo Station - " + name, name + ".local."
}

func multicastInterfaces() ([]*net.Interface, error) {
	all, err := net.Interfaces()
	if err != nil {
		return nil, fmt.Errorf("list network interfaces: %w", err)
	}
	interfaces := make([]*net.Interface, 0, len(all))
	for index := range all {
		networkInterface := &all[index]
		if networkInterface.Flags&net.FlagUp == 0 ||
			networkInterface.Flags&net.FlagMulticast == 0 ||
			networkInterface.Flags&(net.FlagLoopback|net.FlagPointToPoint) != 0 {
			continue
		}
		ips, addressErr := interfaceAddresses(networkInterface)
		if addressErr == nil && len(ips) > 0 {
			interfaces = append(interfaces, networkInterface)
		}
	}
	if len(interfaces) == 0 {
		return nil, errors.New("no active multicast LAN interface")
	}
	return interfaces, nil
}

func interfaceAddresses(networkInterface *net.Interface) ([]net.IP, error) {
	addresses, err := networkInterface.Addrs()
	if err != nil {
		return nil, fmt.Errorf("addresses for interface %s: %w", networkInterface.Name, err)
	}
	ips := make([]net.IP, 0, len(addresses))
	for _, address := range addresses {
		var ip net.IP
		switch value := address.(type) {
		case *net.IPNet:
			ip = value.IP
		case *net.IPAddr:
			ip = value.IP
		}
		if ip == nil || ip.IsLoopback() || !ip.IsGlobalUnicast() {
			continue
		}
		ips = append(ips, ip)
	}
	return ips, nil
}
