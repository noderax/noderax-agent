package system

import (
	"context"
	"net"
	"os"
	"runtime"
	"sort"

	"github.com/shirou/gopsutil/v4/host"
)

type HostDetails struct {
	Hostname        string
	OS              string
	Platform        string
	PlatformVersion string
	KernelVersion   string
	Architecture    string
	IPAddresses     []string
}

func HostInfo(ctx context.Context) (HostDetails, error) {
	hostname, _ := os.Hostname()
	details := HostDetails{
		Hostname:     hostname,
		OS:           runtime.GOOS,
		Architecture: runtime.GOARCH,
	}

	info, err := host.InfoWithContext(ctx)
	if err != nil {
		return details, err
	}

	if info.Hostname != "" {
		details.Hostname = info.Hostname
	}
	if info.OS != "" {
		details.OS = info.OS
	}
	details.Platform = info.Platform
	details.PlatformVersion = info.PlatformVersion
	details.KernelVersion = info.KernelVersion
	details.IPAddresses = localIPAddresses()

	return details, nil
}

func localIPAddresses() []string {
	interfaces, err := net.Interfaces()
	if err != nil {
		return nil
	}

	seen := make(map[string]struct{})
	addresses := make([]string, 0)

	for _, iface := range interfaces {
		if iface.Flags&net.FlagUp == 0 || iface.Flags&net.FlagLoopback != 0 {
			continue
		}

		addrs, err := iface.Addrs()
		if err != nil {
			continue
		}

		for _, addr := range addrs {
			ip := ipFromAddr(addr)
			if ip == nil || ip.IsLoopback() {
				continue
			}
			if ipv4 := ip.To4(); ipv4 != nil {
				ip = ipv4
			}

			value := ip.String()
			if value == "" {
				continue
			}
			if _, exists := seen[value]; exists {
				continue
			}

			seen[value] = struct{}{}
			addresses = append(addresses, value)
		}
	}

	sort.Strings(addresses)
	return addresses
}

func ipFromAddr(addr net.Addr) net.IP {
	switch value := addr.(type) {
	case *net.IPNet:
		return value.IP
	case *net.IPAddr:
		return value.IP
	default:
		return nil
	}
}
