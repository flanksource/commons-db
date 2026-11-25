package kubernetes

import (
	"strings"

	"github.com/flanksource/commons-db/lease"
	"github.com/flanksource/commons-db/lease/leasetest"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	api "k8s.io/api/coordination/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/validation"
	"k8s.io/client-go/kubernetes/fake"
)

const (
	maxNameLength   = 100
	maxHolderLength = 128
)

var invalidNames = []TableEntry{
	Entry("empty", ""),
	Entry("a space", "deploy prod"),
	Entry("a slash", "git/azure"),
	Entry("a colon", "git:azure"),
	Entry("a dot", "git.azure"),
	Entry("a brace", "git{azure}"),
	Entry("non-ASCII", "déploy"),
	Entry("101 characters", strings.Repeat("a", 101)),
}

var _ = Describe("lease names", func() {
	newLocker := func(scope string) (lease.Locker, error) {
		return New(fake.NewClientset().CoordinationV1().Leases("names"), lease.ExpiringOptions{Options: lease.Options{Scope: scope}})
	}
	valid := lease.LockOptions{Holder: "names-spec"}

	DescribeTable("refuses a scope at construction", func(scope string) {
		_, err := newLocker(scope)
		Expect(err).To(MatchError(ContainSubstring("lease scope %q must match", scope)))
	}, invalidNames)

	DescribeTable("refuses a key and a group at acquisition", func(ctx SpecContext, name string) {
		locks, err := newLocker("names")
		Expect(err).NotTo(HaveOccurred())
		_, _, err = locks.Mutex(name, valid).TryLock(ctx)
		Expect(err).To(MatchError(ContainSubstring("lease key %q must match", name)))
		_, err = locks.ReadWriteLock(name, valid).RLock(ctx)
		Expect(err).To(MatchError(ContainSubstring("lease key %q must match", name)))
		_, _, err = locks.GroupLock("country", name, valid).TryLock(ctx)
		Expect(err).To(MatchError(ContainSubstring("lease group %q must match", name)))
		_, err = locks.Mutex(name, valid).Holders(ctx)
		Expect(err).To(MatchError(ContainSubstring("lease key %q must match", name)))
	}, invalidNames)

	DescribeTable("refuses a holder at acquisition", func(ctx SpecContext, holder string) {
		locks, err := newLocker("names")
		Expect(err).NotTo(HaveOccurred())
		_, _, err = locks.Mutex("deploy", lease.LockOptions{Holder: holder}).TryLock(ctx)
		Expect(err).To(MatchError(ContainSubstring("lease holder %q must be nonempty printable UTF-8", holder)))
	},
		Entry("empty", ""),
		Entry("a control character", "ci\nrun"),
		Entry("invalid UTF-8", "ci\xffrun"),
		Entry("129 bytes", strings.Repeat("h", 129)),
	)

	It("accepts names and holders at their length limits", func(ctx SpecContext) {
		longest := strings.Repeat("a", maxNameLength)
		locks, err := newLocker(longest)
		Expect(err).NotTo(HaveOccurred())
		held := leasetest.Hold(ctx, locks.GroupLock(longest, longest, lease.LockOptions{Holder: strings.Repeat("ü", maxHolderLength/2)}))
		Expect(held.Unlock(ctx)).To(Succeed())
	})

	It("gives Kubernetes inputs differing only in case or separator distinct DNS names", func() {
		names := map[string]bool{}
		for _, input := range [][2]string{{"Deploy", "git_oma"}, {"deploy", "git_oma"}, {"deploy", "git-oma"}, {"deploy", "GIT-OMA"}} {
			name, err := kubeName(lease.Request{Scope: input[0], Key: input[1]})
			Expect(err).NotTo(HaveOccurred())
			Expect(name).To(HavePrefix("deploy-git-oma-"))
			Expect(validation.IsDNS1123Subdomain(name)).To(BeEmpty())
			names[name] = true
		}
		Expect(names).To(HaveLen(4))
	})

	It("refuses a Kubernetes scope that cannot start a DNS name", func() {
		_, err := newLocker("_deploy")
		Expect(err).To(MatchError(ContainSubstring("Kubernetes lease name")))
	})

	It("refuses a Kubernetes Lease whose annotation names a different key", func(ctx SpecContext) {
		client := fake.NewClientset()
		locks, err := New(client.CoordinationV1().Leases("names"), lease.ExpiringOptions{Options: lease.Options{Scope: "deploy"}})
		Expect(err).NotTo(HaveOccurred())
		name, err := kubeName(lease.Request{Scope: "deploy", Key: "oma"})
		Expect(err).NotTo(HaveOccurred())
		_, err = client.CoordinationV1().Leases("names").Create(ctx, &api.Lease{ObjectMeta: metav1.ObjectMeta{
			Name:        name,
			Annotations: map[string]string{leaseResourceAnnotation: "deploy:OMA", leaseAnnotation: "{}"},
		}}, metav1.CreateOptions{})
		Expect(err).NotTo(HaveOccurred())
		_, _, err = locks.Mutex("oma", valid).TryLock(ctx)
		Expect(err).To(MatchError(ContainSubstring(`belongs to "deploy:OMA", not "deploy:oma"`)))
		_, err = locks.Mutex("oma", valid).Holders(ctx)
		Expect(err).To(MatchError(ContainSubstring(`belongs to "deploy:OMA", not "deploy:oma"`)))
	})
})
