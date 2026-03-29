package system

import (
	"context"

	"time"

	"github.com/shirou/gopsutil/v4/cpu"
	"github.com/shirou/gopsutil/v4/disk"
	"github.com/shirou/gopsutil/v4/mem"
	gonet "github.com/shirou/gopsutil/v4/net"
	"github.com/shirou/gopsutil/v4/sensors"
)

type Snapshot struct {
	CollectedAt time.Time
	CPU         CPUStats
	Memory      MemoryStats
	Disk        DiskStats
	Networks    []NetworkStats
	Temperature float64
}

type CPUStats struct {
	UsagePercent float64
}

type MemoryStats struct {
	TotalBytes     uint64
	UsedBytes      uint64
	FreeBytes      uint64
	UsedPercent    float64
	AvailableBytes uint64
}

type DiskStats struct {
	Path        string
	TotalBytes  uint64
	UsedBytes   uint64
	FreeBytes   uint64
	UsedPercent float64
}

type NetworkStats struct {
	Interface   string
	BytesSent   uint64
	BytesRecv   uint64
	PacketsSent uint64
	PacketsRecv uint64
	ErrorsIn    uint64
	ErrorsOut   uint64
	DropIn      uint64
	DropOut     uint64
}

type Collector struct {
	diskPath string
}

func NewCollector() *Collector {
	return &Collector{diskPath: defaultDiskPath()}
}

func (c *Collector) Collect(ctx context.Context) (Snapshot, error) {
	cpuPercent, err := cpu.PercentWithContext(ctx, 200*time.Millisecond, false)
	if err != nil {
		return Snapshot{}, err
	}

	memory, err := mem.VirtualMemoryWithContext(ctx)
	if err != nil {
		return Snapshot{}, err
	}

	diskUsage, err := disk.UsageWithContext(ctx, c.diskPath)
	if err != nil {
		return Snapshot{}, err
	}

	networkCounters, err := gonet.IOCountersWithContext(ctx, true)
	if err != nil {
		return Snapshot{}, err
	}

	// Temperature collection is best-effort and should not block the snapshot if it fails
	var maxTemp float64
	temps, err := sensors.TemperaturesWithContext(ctx)
	if err == nil && len(temps) > 0 {
		for _, t := range temps {
			if t.Temperature > maxTemp {
				maxTemp = t.Temperature
			}
		}
		// fmt.Printf("[DEBUG] Collected max temperature: %.2f\n", maxTemp)
	}

	networks := make([]NetworkStats, 0, len(networkCounters))
	for _, item := range networkCounters {
		networks = append(networks, NetworkStats{
			Interface:   item.Name,
			BytesSent:   item.BytesSent,
			BytesRecv:   item.BytesRecv,
			PacketsSent: item.PacketsSent,
			PacketsRecv: item.PacketsRecv,
			ErrorsIn:    item.Errin,
			ErrorsOut:   item.Errout,
			DropIn:      item.Dropin,
			DropOut:     item.Dropout,
		})
	}

	snapshot := Snapshot{
		CollectedAt: time.Now().UTC(),
		CPU: CPUStats{
			UsagePercent: firstFloat(cpuPercent),
		},
		Memory: MemoryStats{
			TotalBytes:     memory.Total,
			UsedBytes:      memory.Used,
			FreeBytes:      memory.Free,
			UsedPercent:    memory.UsedPercent,
			AvailableBytes: memory.Available,
		},
		Disk: DiskStats{
			Path:        c.diskPath,
			TotalBytes:  diskUsage.Total,
			UsedBytes:   diskUsage.Used,
			FreeBytes:   diskUsage.Free,
			UsedPercent: diskUsage.UsedPercent,
		},
		Networks:    networks,
		Temperature: maxTemp,
	}

	return snapshot, nil
}

func defaultDiskPath() string {
	return "/"
}

func firstFloat(values []float64) float64 {
	if len(values) == 0 {
		return 0
	}
	return values[0]
}
