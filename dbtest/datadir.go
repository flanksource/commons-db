package dbtest

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
)

const (
	// EnvDataDir moves the embedded cluster: "tmpfs" puts it on a RAM-backed
	// filesystem (/dev/shm on Linux, a RAM disk volume on macOS); any other
	// value is a literal path.
	EnvDataDir = "COMMONS_DB_DATA_DIR"
	// EnvBinDir overrides where the PostgreSQL binaries are extracted. They are
	// never put on tmpfs, so they survive a reboot.
	EnvBinDir = "COMMONS_DB_BIN_DIR"
	// EnvTmpfsSize is the RAM-backed filesystem size COMMONS_DB_DATA_DIR=tmpfs
	// requires, e.g. "4GB". Defaults to 2GB.
	EnvTmpfsSize = "COMMONS_DB_TMPFS_SIZE"

	tmpfsDataDir     = "tmpfs"
	ramDiskVolume    = "commons-db"
	defaultTmpfsSize = uint64(2 << 30)
)

var byteSizePattern = regexp.MustCompile(`^([0-9]+)\s*([kKmMgGtT]?)(?:i?[bB])?$`)

// embeddedDirs is where the shared embedded server keeps its cluster and
// binaries. RAMDiskBytes is the RAM-backed filesystem size, or zero on disk.
type embeddedDirs struct {
	DataDir      string
	BinariesDir  string
	RAMDiskBytes uint64
}

// filesystemStat is the part of statfs that tmpfs validation needs.
type filesystemStat struct {
	Tmpfs         bool
	CapacityBytes uint64
}

// resolveEmbeddedDirs picks the cluster directory from Options.DataDir, then
// COMMONS_DB_DATA_DIR, then $TMPDIR/commons-db. Binaries always stay on disk.
func resolveEmbeddedDirs(dataDir string) (embeddedDirs, error) {
	root := filepath.Join(os.TempDir(), "commons-db")
	dirs := embeddedDirs{DataDir: root, BinariesDir: filepath.Join(root, "bin")}
	if binDir := os.Getenv(EnvBinDir); binDir != "" {
		dirs.BinariesDir = binDir
	}
	switch env := os.Getenv(EnvDataDir); {
	case dataDir != "":
		dirs.DataDir = dataDir
	case env == tmpfsDataDir:
		size, err := tmpfsSize()
		if err != nil {
			return embeddedDirs{}, err
		}
		if dirs.DataDir, err = ramBackedDataDir(ramDiskVolume, size, root); err != nil {
			return embeddedDirs{}, err
		}
		dirs.RAMDiskBytes = size
	case env != "":
		dirs.DataDir = env
	}
	return dirs, nil
}

func tmpfsSize() (uint64, error) {
	raw := os.Getenv(EnvTmpfsSize)
	if raw == "" {
		return defaultTmpfsSize, nil
	}
	size, err := parseByteSize(raw)
	if err != nil {
		return 0, fmt.Errorf("%s %q: %w", EnvTmpfsSize, raw, err)
	}
	return size, nil
}

// parseByteSize reads a size such as "2GB", "512m" or "1GiB" in binary units.
func parseByteSize(value string) (uint64, error) {
	match := byteSizePattern.FindStringSubmatch(strings.TrimSpace(value))
	if match == nil {
		return 0, fmt.Errorf("want a size such as 2GB or 512MB")
	}
	number, err := strconv.ParseUint(match[1], 10, 64)
	if err != nil {
		return 0, err
	}
	shift := map[string]uint{"": 0, "k": 10, "m": 20, "g": 30, "t": 40}[strings.ToLower(match[2])]
	return number << shift, nil
}

func validateTmpfs(path string, stat filesystemStat, required uint64) error {
	if !stat.Tmpfs {
		return fmt.Errorf("%s is not a tmpfs mount; %s=tmpfs needs one", path, EnvDataDir)
	}
	if stat.CapacityBytes < required {
		return fmt.Errorf(
			"%s holds %dMB but %s requires %dMB; grow it (docker run --shm-size=%dm) or lower %s",
			path, stat.CapacityBytes>>20, EnvTmpfsSize, required>>20, required>>20, EnvTmpfsSize,
		)
	}
	return nil
}
