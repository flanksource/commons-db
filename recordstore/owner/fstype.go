// Refusing network filesystems: file locks and SQLite's WAL are unreliable
// there, so an owner elected on one could share its store with another.
package owner

import (
	"fmt"
	"strings"
)

// networkFilesystems are the filesystems a store may not live on, by the name
// filesystemName reports.
var networkFilesystems = map[string]bool{
	"nfs": true, "nfs4": true, "smb": true, "smb2": true, "smbfs": true, "cifs": true, "9p": true,
	"fuse": true, "virtiofs": true, "ceph": true, "afpfs": true, "webdav": true,
}

// NetworkFilesystem reports whether a filesystem of type name is one a store
// may not live on: a network filesystem, or any FUSE one, whose locking is
// whatever the userspace server makes of it.
func NetworkFilesystem(name string) bool {
	return networkFilesystems[name] || strings.HasSuffix(name, "fuse") || strings.HasPrefix(name, "fuse.")
}

// RefuseNetworkFilesystem refuses dir when it is on a network filesystem. A
// platform that cannot name its filesystems accepts every dir.
func RefuseNetworkFilesystem(dir string) error {
	name, err := filesystemName(dir)
	if err != nil {
		return fmt.Errorf("record store owner: inspect the filesystem of %s: %w", dir, err)
	}
	if NetworkFilesystem(name) {
		return fmt.Errorf("record store owner: %s is on a %s filesystem, where file locks and SQLite's WAL are unreliable; keep record stores on a local disk", dir, name)
	}
	return nil
}
