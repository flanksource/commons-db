// The filesystem type of a directory on Linux, named from statfs's magic
// number.
package owner

import (
	"fmt"

	"golang.org/x/sys/unix"
)

// linuxFilesystems names the statfs magic numbers of the filesystems a store
// is refused on, and of the common local ones.
var linuxFilesystems = map[uint32]string{
	0x6969:     "nfs",
	0x517b:     "smb",
	0xfe534d42: "smb2",
	0xff534d42: "cifs",
	0x01021997: "9p",
	0x65735546: "fuse",
	0x00c36400: "ceph",
	0xef53:     "ext4",
	0x58465342: "xfs",
	0x9123683e: "btrfs",
	0x01021994: "tmpfs",
	0x794c7630: "overlay",
	0x2fc12fc1: "zfs",
}

func filesystemName(dir string) (string, error) {
	var stat unix.Statfs_t
	if err := unix.Statfs(dir, &stat); err != nil {
		return "", err
	}
	magic := uint32(stat.Type)
	if name, ok := linuxFilesystems[magic]; ok {
		return name, nil
	}
	return fmt.Sprintf("0x%x", magic), nil
}
