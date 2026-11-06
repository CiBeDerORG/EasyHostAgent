//go:build linux

package host

import (
	"bufio"
	"os"
	"strconv"
	"strings"
)

// Linux / fnOS 保持原有 /proc 内存统计方式。
func memory() (total, used uint64) {
	f, err := os.Open("/proc/meminfo")
	if err != nil {
		return 0, 0
	}
	defer f.Close()
	var available uint64
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		fields := strings.Fields(scanner.Text())
		if len(fields) < 2 {
			continue
		}
		n, e := strconv.ParseUint(fields[1], 10, 64)
		if e != nil {
			continue
		}
		switch fields[0] {
		case "MemTotal:":
			total = n * 1024
		case "MemAvailable:":
			available = n * 1024
		}
	}
	if total >= available {
		used = total - available
	}
	return total, used
}
