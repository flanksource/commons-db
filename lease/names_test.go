package lease

import (
	"strings"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	api "k8s.io/api/coordination/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/validation"
	"k8s.io/client-go/kubernetes/fake"
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
	newLocker := func(scope string) (Locker, error) {
		return NewKubernetes(fake.NewClientset().CoordinationV1().Leases("names"), ExpiringOptions{Options: Options{Scope: scope}})
	}
	valid := LockOptions{Holder: "names-spec"}

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
		_, _, err = locks.Mutex("deploy", LockOptions{Holder: holder}).TryLock(ctx)
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
		held := hold(ctx, locks.GroupLock(longest, longest, LockOptions{Holder: strings.Repeat("ü", maxHolderLength/2)}))
		Expect(held.Unlock(ctx)).To(Succeed())
	})

	It("names Redis keys by scope and key inside one hash tag", func() {
		Expect(redisKeys(request{scope: "deploy", key: "git-azure_oma"})).To(Equal([]string{
			"commons-db:lease:{deploy:git-azure_oma}:writer",
			"commons-db:lease:{deploy:git-azure_oma}:shared",
			"commons-db:lease:{deploy:git-azure_oma}:waiting",
			"commons-db:lease:{deploy:git-azure_oma}:grouped",
			"commons-db:lease:{deploy:git-azure_oma}:holders",
		}))
	})

	It("names SQL Server app locks by scope and key within the resource limit", func() {
		Expect(sqlResource(request{scope: "deploy", key: "git-azure_oma"})).To(Equal("deploy:git-azure_oma"))
		longest := strings.Repeat("a", maxNameLength)
		name, err := sqlResource(request{scope: longest, key: longest})
		Expect(err).NotTo(HaveOccurred())
		Expect(len(name + sqlLongestSuffix)).To(BeNumerically("<=", sqlResourceLimit))
	})

	It("gives Kubernetes inputs differing only in case or separator distinct DNS names", func() {
		names := map[string]bool{}
		for _, input := range [][2]string{{"Deploy", "git_oma"}, {"deploy", "git_oma"}, {"deploy", "git-oma"}, {"deploy", "GIT-OMA"}} {
			name, err := kubeName(request{scope: input[0], key: input[1]})
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
		locks, err := NewKubernetes(client.CoordinationV1().Leases("names"), ExpiringOptions{Options: Options{Scope: "deploy"}})
		Expect(err).NotTo(HaveOccurred())
		name, err := kubeName(request{scope: "deploy", key: "oma"})
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
