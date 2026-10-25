package agent

import (
	"bufio"
	"os"
	"runtime"
	"strconv"
	"strings"

	"github.com/sh4rpx88/Watchtower/internal/protocol"
)

// hostStats describes the machine for heartbeats. Values it can't read are 0.
func hostStats(uptime int64, dataDir string) protocol.Heartbeat {
	hb := protocol.Heartbeat{UptimeSeconds: uptime, NumCPU: runtime.NumCPU(), MemoryTotal: memoryTotal()}
	hb.DiskTotal, hb.DiskFree = diskSpace(dataDir)
	return hb
}

// memoryTotal reads MemTotal from /proc/meminfo on Linux.
func memoryTotal() int64 {
	f, err := os.Open("/proc/meminfo")
	if err != nil {
		return 0
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		fields := strings.Fields(sc.Text())
		if len(fields) >= 2 && fields[0] == "MemTotal:" {
			kb, _ := strconv.ParseInt(fields[1], 10, 64)
			return kb * 1024
		}
	}
	return 0
}
