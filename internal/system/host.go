package system

import (
	"context"
	"os"
	"runtime"

	"github.com/shirou/gopsutil/v4/host"
)

type HostDetails struct {
	Hostname        string
	OS              string
	Platform        string
	PlatformVersion string
	KernelVersion   string
	Architecture    string
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

	return details, nil
}
