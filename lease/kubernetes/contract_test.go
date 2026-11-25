package kubernetes

import (
	"context"
	"strconv"
	"testing"
	"time"

	"github.com/flanksource/commons-db/lease"
	"github.com/flanksource/commons-db/lease/leasetest"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	api "k8s.io/api/coordination/v1"
	core "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/kubernetes/fake"
	clienttest "k8s.io/client-go/testing"
	"k8s.io/client-go/tools/clientcmd"
)

func TestKubernetesLease(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "Kubernetes Lease Suite")
}

// newFakeLeases returns a fake Lease client that rejects stale resource
// versions, as the API server does.
func newFakeLeases() *fake.Clientset {
	client := fake.NewClientset()
	client.PrependReactor("update", "leases", func(action clienttest.Action) (bool, runtime.Object, error) {
		next := action.(clienttest.UpdateAction).GetObject().(*api.Lease).DeepCopy()
		stored, err := client.Tracker().Get(action.GetResource(), action.GetNamespace(), next.Name)
		if err != nil {
			return true, nil, err
		}
		current := stored.(*api.Lease).ResourceVersion
		if next.ResourceVersion != current {
			return true, nil, apierrors.NewConflict(action.GetResource().GroupResource(), next.Name, nil)
		}
		version, _ := strconv.Atoi(current)
		next.ResourceVersion = strconv.Itoa(version + 1)
		return true, next, client.Tracker().Update(action.GetResource(), next, action.GetNamespace())
	})
	return client
}

var _ = Describe("lease backends", func() {
	client := newFakeLeases()
	leasetest.LockerContract("Kubernetes", leasetest.AllModes, func(options lease.ExpiringOptions) (lease.Locker, error) {
		return New(client.CoordinationV1().Leases("lease-test"), options)
	})
})

var _ = Describe("lease holders", func() {
	client := newFakeLeases()
	leasetest.HoldersContract("Kubernetes", leasetest.HolderBackend{NamesGroups: true, Lose: leasetest.ExpireUnrenewed, Factory: func(options lease.ExpiringOptions) (lease.Locker, error) {
		return New(client.CoordinationV1().Leases("lease-holders"), options)
	}})
})

var _ = Describe("live lease backends", Label("integration"), func() {
	leasetest.LockerContract("Kubernetes API server", leasetest.AllModes, func(options lease.ExpiringOptions) (lease.Locker, error) {
		config, err := clientcmd.BuildConfigFromFlags("", leasetest.TestSetting("COMMONS_DB_LEASE_KUBECONFIG"))
		Expect(err).NotTo(HaveOccurred())
		client, err := kubernetes.NewForConfig(config)
		Expect(err).NotTo(HaveOccurred())
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		name := "lease-" + options.Scope
		namespaces := client.CoreV1().Namespaces()
		_, err = namespaces.Create(ctx, &core.Namespace{ObjectMeta: metav1.ObjectMeta{Name: name}}, metav1.CreateOptions{})
		if !apierrors.IsAlreadyExists(err) {
			Expect(err).NotTo(HaveOccurred())
			DeferCleanup(func(ctx SpecContext) error { return namespaces.Delete(ctx, name, metav1.DeleteOptions{}) })
		}
		return New(client.CoordinationV1().Leases(name), options)
	})
})
