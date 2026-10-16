package db

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"os"
	"runtime"
	"strconv"
	"strings"
)

const (
	mib = uint64(1 << 20)
	gib = uint64(1 << 30)
)

// HostLimits is the CPU and memory budget a disposable test PostgreSQL server
// sizes itself to.
type HostLimits struct {
	// CPUs is runtime.GOMAXPROCS(0), which follows the cgroup CPU quota.
	CPUs int
	// MemoryBytes is the smaller of the host's memory and the cgroup limit.
	MemoryBytes uint64
	// CI marks a dedicated runner (CI=true), which may give PostgreSQL a larger
	// share of memory than a shared workstation.
	CI bool
}

// DetectHostLimits reads the CPU, memory and CI limits of the current process.
// Memory that cannot be read is an error naming the source, never a default.
func DetectHostLimits() (HostLimits, error) {
	memory, err := detectMemoryBytes()
	if err != nil {
		return HostLimits{}, err
	}
	return HostLimits{
		CPUs:        runtime.GOMAXPROCS(0),
		MemoryBytes: memory,
		CI:          os.Getenv("CI") == "true",
	}, nil
}

// ScaledTestingParameters sizes a fast-testing server to limits. ramDiskBytes
// is the size of the RAM-backed filesystem holding the cluster, or zero when it
// is on disk; WAL kept until the next checkpoint occupies memory there.
func ScaledTestingParameters(limits HostLimits, ramDiskBytes uint64) map[string]string {
	return map[string]string{
		"max_connections":           strconv.Itoa(min(max(limits.CPUs*50, 200), 1000)),
		"shared_buffers":            megabytes(sharedBuffersBytes(limits)),
		"max_locks_per_transaction": "256",
		"maintenance_work_mem":      "256MB",
		"work_mem":                  "8MB",
		"jit":                       "off",
		"wal_level":                 "minimal",
		"max_wal_senders":           "0",
		"checkpoint_timeout":        "1d",
		"max_wal_size":              megabytes(maxWALSizeBytes(ramDiskBytes)),
		"wal_buffers":               "64MB",
	}
}

func sharedBuffersBytes(limits HostLimits) uint64 {
	share, ceiling := limits.MemoryBytes/16, 2*gib
	if limits.CI {
		share, ceiling = limits.MemoryBytes/4, 4*gib
	}
	return max(min(share, ceiling), 128*mib)
}

func maxWALSizeBytes(ramDiskBytes uint64) uint64 {
	if ramDiskBytes == 0 {
		return 8 * gib
	}
	return min(8*gib, ramDiskBytes/4)
}

func megabytes(bytes uint64) string {
	return strconv.FormatUint(bytes/mib, 10) + "MB"
}

// linuxMemoryBytes returns the smaller of MemTotal and the cgroup memory limit.
func linuxMemoryBytes(cgroupV2Path, cgroupV1Path, meminfoPath string) (uint64, error) {
	total, err := meminfoTotal(meminfoPath)
	if err != nil {
		return 0, err
	}
	limit, limited, err := cgroupMemoryLimit(cgroupV2Path, cgroupV1Path)
	if err != nil {
		return 0, err
	}
	if limited {
		return min(limit, total), nil
	}
	return total, nil
}

// cgroupMemoryLimit reads the v2 limit, then the v1 limit. limited is false
// when the limit is "max" or neither file exists, as in a host's root cgroup.
func cgroupMemoryLimit(v2Path, v1Path string) (limit uint64, limited bool, err error) {
	for _, path := range []string{v2Path, v1Path} {
		raw, err := os.ReadFile(path)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return 0, false, fmt.Errorf("read cgroup memory limit %s: %w", path, err)
		}
		value := strings.TrimSpace(string(raw))
		if value == "max" {
			return 0, false, nil
		}
		limit, err := strconv.ParseUint(value, 10, 64)
		if err != nil {
			return 0, false, fmt.Errorf("parse cgroup memory limit %s: %w", path, err)
		}
		return limit, true, nil
	}
	return 0, false, nil
}

func meminfoTotal(path string) (uint64, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return 0, fmt.Errorf("read %s: %w", path, err)
	}
	scanner := bufio.NewScanner(bytes.NewReader(raw))
	for scanner.Scan() {
		fields := strings.Fields(scanner.Text())
		if len(fields) != 3 || fields[0] != "MemTotal:" || fields[2] != "kB" {
			continue
		}
		kilobytes, err := strconv.ParseUint(fields[1], 10, 64)
		if err != nil {
			return 0, fmt.Errorf("parse MemTotal in %s: %w", path, err)
		}
		return kilobytes * 1024, nil
	}
	return 0, fmt.Errorf("%s has no MemTotal line in kB", path)
}
