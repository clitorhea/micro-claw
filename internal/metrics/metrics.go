package metrics

import (
	"bufio"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"syscall"
	"time"
)

func GetSystemMetrics() (cpu, mem, disk float64, err error) {
	cpu, err = getCPUUsage()
	if err != nil {
		return 0, 0, 0, fmt.Errorf("failed to get CPU: %w", err)
	}

	mem, err = getMemoryUsage()
	if err != nil {
		return 0, 0, 0, fmt.Errorf("failed to get Memory: %w", err)
	}

	disk, err = getDiskUsage()
	if err != nil {
		return 0, 0, 0, fmt.Errorf("failed to get Disk: %w", err)
	}

	return cpu, mem, disk, nil
}

func GetTopProcesses() (string, error) {
	cmd := exec.Command("top", "-b", "-n", "1")
	out, err := cmd.Output()
	if err != nil {
		return "", err
	}
	lines := strings.Split(string(out), "\n")
	limit := 15
	if len(lines) < limit {
		limit = len(lines)
	}
	return strings.Join(lines[:limit], "\n"), nil
}

func readCPUStats() (total, idle uint64, err error) {
	file, err := os.Open("/proc/stat")
	if err != nil {
		return 0, 0, err
	}
	defer file.Close()

	var user, nice, system, idleVal, iowait, irq, softirq, steal, guest, guestnice uint64
	_, err = fmt.Fscanf(file, "cpu %d %d %d %d %d %d %d %d %d %d",
		&user, &nice, &system, &idleVal, &iowait, &irq, &softirq, &steal, &guest, &guestnice)
	if err != nil {
		return 0, 0, err
	}

	total = user + nice + system + idleVal + iowait + irq + softirq + steal
	idle = idleVal + iowait
	return total, idle, nil
}

func getCPUUsage() (float64, error) {
	t1, idle1, err := readCPUStats()
	if err != nil {
		return 0, err
	}
	time.Sleep(500 * time.Millisecond)
	t2, idle2, err := readCPUStats()
	if err != nil {
		return 0, err
	}
	if t2 == t1 {
		return 0.0, nil
	}
	return float64((t2-t1)-(idle2-idle1)) / float64(t2-t1) * 100.0, nil
}

func getMemoryUsage() (float64, error) {
	file, err := os.Open("/proc/meminfo")
	if err != nil {
		return 0, err
	}
	defer file.Close()

	var memTotal, memAvailable uint64
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		line := scanner.Text()
		var val uint64
		if strings.HasPrefix(line, "MemTotal:") {
			_, err = fmt.Sscanf(line, "MemTotal: %d kB", &val)
			if err == nil {
				memTotal = val
			}
		} else if strings.HasPrefix(line, "MemAvailable:") {
			_, err = fmt.Sscanf(line, "MemAvailable: %d kB", &val)
			if err == nil {
				memAvailable = val
			}
		}
	}

	if memTotal == 0 {
		return 0, fmt.Errorf("could not parse MemTotal from /proc/meminfo")
	}

	// Fallback for older kernels without MemAvailable
	if memAvailable == 0 {
		_, _ = file.Seek(0, 0)
		scanner = bufio.NewScanner(file)
		var memFree, cached, buffers uint64
		for scanner.Scan() {
			line := scanner.Text()
			var val uint64
			if strings.HasPrefix(line, "MemFree:") {
				_, _ = fmt.Sscanf(line, "MemFree: %d kB", &val)
				memFree = val
			} else if strings.HasPrefix(line, "Cached:") {
				_, _ = fmt.Sscanf(line, "Cached: %d kB", &val)
				cached = val
			} else if strings.HasPrefix(line, "Buffers:") {
				_, _ = fmt.Sscanf(line, "Buffers: %d kB", &val)
				buffers = val
			}
		}
		memAvailable = memFree + cached + buffers
	}

	used := memTotal - memAvailable
	return float64(used) / float64(memTotal) * 100.0, nil
}

func getDiskUsage() (float64, error) {
	path := "/host"
	if _, err := os.Stat(path); os.IsNotExist(err) {
		path = "/"
	}
	var stat syscall.Statfs_t
	err := syscall.Statfs(path, &stat)
	if err != nil {
		return 0, err
	}
	all := stat.Blocks * uint64(stat.Bsize)
	free := stat.Bfree * uint64(stat.Bsize)
	used := all - free
	if all == 0 {
		return 0, nil
	}
	return float64(used) / float64(all) * 100.0, nil
}
