// Specs for what election rests on: the exclusive lock, the state file that
// names its holder, and where the control socket lives.
package owner_test

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/flanksource/commons-db/recordstore/owner"
)

var _ = Describe("Exclusive", func() {
	var path string

	BeforeEach(func() {
		path = filepath.Join(GinkgoT().TempDir(), "ndjson")
	})

	It("holds a path for one holder, naming it to anyone else who asks", func() {
		release, err := owner.Exclusive(path, "build-1")
		Expect(err).ToNot(HaveOccurred())

		_, err = owner.Exclusive(path, "build-2")
		Expect(errors.Is(err, owner.ErrLocked)).To(BeTrue(), "Exclusive: %v", err)
		var locked *owner.LockedError
		Expect(errors.As(err, &locked)).To(BeTrue())
		Expect(locked.Path).To(Equal(path))
		Expect(locked.Owner).ToNot(BeNil())
		Expect(locked.Owner.PID).To(Equal(os.Getpid()))
		Expect(locked.Owner.Build).To(Equal("build-1"))
		Expect(err.Error()).To(ContainSubstring("pid"))

		Expect(release()).To(Succeed())
		state, found, err := owner.ReadState(path)
		Expect(err).ToNot(HaveOccurred())
		Expect(found).To(BeFalse(), "releasing removes the holder's state: %+v", state)
		again, err := owner.Exclusive(path, "build-2")
		Expect(err).ToNot(HaveOccurred())
		Expect(again()).To(Succeed())
	})
})

var _ = Describe("state", func() {
	It("reports a path nobody holds as having no state", func() {
		_, found, err := owner.ReadState(filepath.Join(GinkgoT().TempDir(), "records.sqlite"))
		Expect(err).ToNot(HaveOccurred())
		Expect(found).To(BeFalse())
	})

	It("keeps the control socket beside the store while its path fits a socket address", func() {
		path := filepath.Join(GinkgoT().TempDir(), "records.sqlite")
		Expect(owner.SocketPath(path)).To(Equal(path + ".sock"))
	})

	It("moves the control socket of a deep path into the temp dir, the same for every process", func() {
		path := filepath.Join(GinkgoT().TempDir(), strings.Repeat("d", 120), "records.sqlite")
		socket := owner.SocketPath(path)
		Expect(len(socket)).To(BeNumerically("<=", 100))
		Expect(filepath.Dir(socket)).To(Equal(filepath.Clean(os.TempDir())))
		Expect(owner.SocketPath(path)).To(Equal(socket))
		Expect(owner.SocketPath(path + "2")).ToNot(Equal(socket))
	})
})

var _ = Describe("network filesystems", func() {
	DescribeTable("refuses the filesystems flock and WAL are unreliable on",
		func(name string, network bool) {
			Expect(owner.NetworkFilesystem(name)).To(Equal(network))
		},
		Entry("nfs", "nfs", true),
		Entry("smb", "smb", true),
		Entry("cifs", "cifs", true),
		Entry("9p", "9p", true),
		Entry("fuse", "fuse", true),
		Entry("virtiofs", "virtiofs", true),
		Entry("ceph", "ceph", true),
		Entry("smbfs", "smbfs", true),
		Entry("afpfs", "afpfs", true),
		Entry("webdav", "webdav", true),
		Entry("osxfuse", "osxfuse", true),
		Entry("ext4", "ext4", false),
		Entry("apfs", "apfs", false),
		Entry("tmpfs", "tmpfs", false),
	)

	It("accepts the local temp dir", func() {
		Expect(owner.RefuseNetworkFilesystem(GinkgoT().TempDir())).To(Succeed())
	})
})

var _ = Describe("control socket", func() {
	var socket string

	BeforeEach(func() {
		socket = filepath.Join(GinkgoT().TempDir(), "records.sqlite.sock")
	})

	serve := func(instance string, ingest func(ids []string, wait bool) ([]string, error)) *owner.ControlServer {
		server, err := owner.ServeControl(socket, owner.ControlHandler{
			Status: func() owner.State { return owner.State{Instance: instance, Phase: owner.PhaseReady} },
			Ingest: ingest,
		})
		Expect(err).ToNot(HaveOccurred())
		DeferCleanup(server.Close)
		return server
	}

	It("answers status and ingest requests, one per connection", func() {
		var poked []string
		serve("owner-1", func(ids []string, wait bool) ([]string, error) {
			poked = append(poked, ids...)
			if wait {
				return ids, nil
			}
			return nil, nil
		})

		state, err := owner.ControlStatus(socket, time.Second)
		Expect(err).ToNot(HaveOccurred())
		Expect(state.Instance).To(Equal("owner-1"))
		ingested, err := owner.ControlIngest(socket, []string{"b-1"}, true, time.Second)
		Expect(err).ToNot(HaveOccurred())
		Expect(ingested).To(Equal([]string{"b-1"}))
		Expect(poked).To(Equal([]string{"b-1"}))

		info, err := os.Stat(socket)
		Expect(err).ToNot(HaveOccurred())
		Expect(info.Mode().Perm()).To(Equal(os.FileMode(0o600)))
	})

	It("reports a handler's error to the client", func() {
		serve("owner-1", func([]string, bool) ([]string, error) { return nil, errors.New("draining") })
		_, err := owner.ControlIngest(socket, []string{"b-1"}, false, time.Second)
		Expect(err).To(MatchError(ContainSubstring("draining")))
	})

	It("replaces a socket file nothing listens on, and refuses one something does", func() {
		Expect(os.WriteFile(socket, nil, 0o600)).To(Succeed())
		serve("owner-1", func([]string, bool) ([]string, error) { return nil, nil })

		_, err := owner.ServeControl(socket, owner.ControlHandler{Status: func() owner.State { return owner.State{} }})
		Expect(err).To(MatchError(ContainSubstring("already")))
	})

	It("unlinks its socket on close only while the socket is still its own", func() {
		server := serve("owner-1", func([]string, bool) ([]string, error) { return nil, nil })
		Expect(server.Close()).To(Succeed())
		Expect(socket).ToNot(BeAnExistingFile())

		_, err := owner.ControlStatus(socket, 100*time.Millisecond)
		Expect(err).To(HaveOccurred())
	})
})
