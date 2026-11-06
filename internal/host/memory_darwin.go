//go:build darwin

package host

import (
    "bufio"
    "context"
    "os/exec"
    "regexp"
    "strconv"
    "strings"
    "time"
)

var pageSizePattern = regexp.MustCompile(`page size of ([0-9]+) bytes`)

// macOS 使用系统原生命令读取物理内存，避免将 Linux /proc 值误报成零。
// used 是 VM 页统计估算值，不等同于活动监视器的内存压力或压缩内存指标。
func memory() (total, used uint64) {
    ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
    defer cancel()
    raw, err := exec.CommandContext(ctx, "/usr/sbin/sysctl", "-n", "hw.memsize").Output()
    if err != nil { return 0, 0 }
    total, err = strconv.ParseUint(strings.TrimSpace(string(raw)), 10, 64)
    if err != nil { return 0, 0 }
    pages, err := exec.CommandContext(ctx, "/usr/bin/vm_stat").Output()
    if err != nil { return total, 0 }
    return total, usedFromVMStat(total, string(pages))
}

func usedFromVMStat(total uint64, raw string) uint64 {
    // vm_stat 输出的首行包含实际页大小（Apple Silicon 常见为 16384）。
    match := pageSizePattern.FindStringSubmatch(raw)
    if len(match) != 2 { return 0 }
    pageSize, err := strconv.ParseUint(match[1], 10, 64)
    if err != nil || pageSize == 0 { return 0 }
    var available uint64
    scanner := bufio.NewScanner(strings.NewReader(raw))
    for scanner.Scan() {
        line := strings.TrimSpace(scanner.Text())
        for _, key := range []string{"Pages free:", "Pages inactive:", "Pages speculative:", "Pages purgeable:"} {
            if !strings.HasPrefix(line, key) { continue }
            rawCount := strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(strings.TrimPrefix(line, key)), "."))
            pages, err := strconv.ParseUint(rawCount, 10, 64)
            if err == nil && pages <= total/pageSize { available += pages }
            break
        }
    }
    if available >= total/pageSize { return 0 }
    return total - available*pageSize
}
