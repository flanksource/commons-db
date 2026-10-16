package db

const (
	cgroupV2MemoryMax   = "/sys/fs/cgroup/memory.max"
	cgroupV1MemoryLimit = "/sys/fs/cgroup/memory/memory.limit_in_bytes"
	procMeminfo         = "/proc/meminfo"
)

func detectMemoryBytes() (uint64, error) {
	return linuxMemoryBytes(cgroupV2MemoryMax, cgroupV1MemoryLimit, procMeminfo)
}
