package dbtest

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/gofrs/flock"
	"golang.org/x/sys/unix"
)

const (
	ramDiskLockTimeout   = 2 * time.Minute
	ramDiskSectorSize    = 512
	hdiutilImageBoundary = "================================================"
)

// ramDiskImage is an attached RAM-backed disk image.
type ramDiskImage struct {
	Device string
	Bytes  uint64
}

// ramBackedDataDir returns /Volumes/<name>, reusing a RAM disk already mounted
// there or creating one of size bytes. lockDir serialises creation across
// processes. The disk does not need root and is lost on reboot.
func ramBackedDataDir(name string, size uint64, lockDir string) (string, error) {
	if err := os.MkdirAll(lockDir, 0o750); err != nil {
		return "", fmt.Errorf("create RAM disk lock directory %s: %w", lockDir, err)
	}
	lock := flock.New(filepath.Join(lockDir, "ramdisk-"+name+".lock"))
	ctx, cancel := context.WithTimeout(context.Background(), ramDiskLockTimeout)
	defer cancel()
	if _, err := lock.TryLockContext(ctx, 100*time.Millisecond); err != nil {
		return "", fmt.Errorf("lock RAM disk creation %s: %w", lock.Path(), err)
	}
	mountPoint := filepath.Join("/Volumes", name)
	err := ensureRAMDisk(name, mountPoint, size)
	return mountPoint, errors.Join(err, lock.Unlock())
}

func ensureRAMDisk(name, mountPoint string, size uint64) error {
	var stat unix.Statfs_t
	err := unix.Statfs(mountPoint, &stat)
	if err != nil && !errors.Is(err, unix.ENOENT) {
		return fmt.Errorf("statfs %s: %w", mountPoint, err)
	}
	if err == nil && unix.ByteSliceToString(stat.Mntonname[:]) == mountPoint {
		return verifyRAMDisk(mountPoint, size)
	}
	return createRAMDisk(name, size)
}

func verifyRAMDisk(mountPoint string, size uint64) error {
	image, err := attachedRAMDisk(mountPoint)
	if err != nil {
		return err
	}
	if image == nil {
		return fmt.Errorf("%s is mounted but is not a RAM disk; unmount it or set %s to a path", mountPoint, EnvDataDir)
	}
	if image.Bytes < size {
		return fmt.Errorf(
			"RAM disk %s (%dMB) is smaller than %s=%dMB; detach it with `hdiutil detach %s` to recreate it",
			mountPoint, image.Bytes>>20, EnvTmpfsSize, size>>20, image.Device,
		)
	}
	return nil
}

func createRAMDisk(name string, size uint64) error {
	sectors := (size + ramDiskSectorSize - 1) / ramDiskSectorSize
	out, err := exec.Command("hdiutil", "attach", "-nobrowse", "-nomount", "ram://"+strconv.FormatUint(sectors, 10)).Output()
	if err != nil {
		return fmt.Errorf("attach %dMB RAM disk: %w", size>>20, commandError(err))
	}
	device := strings.TrimSpace(string(out))
	if out, err := exec.Command("diskutil", "erasevolume", "APFS", name, device).CombinedOutput(); err != nil {
		return errors.Join(
			fmt.Errorf("format RAM disk %s as %s: %w: %s", device, name, err, out),
			exec.Command("hdiutil", "detach", device).Run(),
		)
	}
	return nil
}

// detachRAMDisk ejects the RAM disk mounted at mountPoint, discarding its data.
func detachRAMDisk(mountPoint string) error {
	image, err := attachedRAMDisk(mountPoint)
	if err != nil {
		return err
	}
	if image == nil {
		return fmt.Errorf("no RAM disk is mounted at %s", mountPoint)
	}
	if out, err := exec.Command("hdiutil", "detach", image.Device, "-force").CombinedOutput(); err != nil {
		return fmt.Errorf("detach RAM disk %s: %w: %s", image.Device, err, out)
	}
	return nil
}

func attachedRAMDisk(mountPoint string) (*ramDiskImage, error) {
	out, err := exec.Command("hdiutil", "info").Output()
	if err != nil {
		return nil, fmt.Errorf("list attached disk images: %w", commandError(err))
	}
	return ramDiskImageAt(string(out), mountPoint)
}

// ramDiskImageAt finds, in `hdiutil info` output, the ram:// image with a
// volume mounted at mountPoint. It returns nil when there is none.
func ramDiskImageAt(info, mountPoint string) (*ramDiskImage, error) {
	for _, block := range strings.Split(info, hdiutilImageBoundary) {
		fields := map[string]string{}
		var devices []string
		mounted := false
		for _, line := range strings.Split(block, "\n") {
			if strings.HasPrefix(line, "/dev/") {
				columns := strings.Split(line, "\t")
				devices = append(devices, columns[0])
				mounted = mounted || columns[len(columns)-1] == mountPoint
				continue
			}
			if key, value, ok := strings.Cut(line, ":"); ok {
				fields[strings.TrimSpace(key)] = strings.TrimSpace(value)
			}
		}
		if !mounted || !strings.HasPrefix(fields["image-path"], "ram://") {
			continue
		}
		blocks, blocksErr := strconv.ParseUint(fields["blockcount"], 10, 64)
		blockSize, sizeErr := strconv.ParseUint(fields["blocksize"], 10, 64)
		if err := errors.Join(blocksErr, sizeErr); err != nil {
			return nil, fmt.Errorf("parse size of RAM disk at %s from hdiutil info: %w", mountPoint, err)
		}
		return &ramDiskImage{Device: devices[0], Bytes: blocks * blockSize}, nil
	}
	return nil, nil
}

func commandError(err error) error {
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) && len(exitErr.Stderr) > 0 {
		return fmt.Errorf("%w: %s", err, strings.TrimSpace(string(exitErr.Stderr)))
	}
	return err
}
