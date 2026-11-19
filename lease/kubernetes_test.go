package lease

import (
	"encoding/json"
	"strconv"
	"strings"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	api "k8s.io/api/coordination/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/validation"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes/fake"
	clienttest "k8s.io/client-go/testing"
)

var _ = Describe("Kubernetes lease holder records", func() {
	It("retains expiry evidence through sparse observations of many lease objects", func() {
		backend := &kubeBackend{observations: make(map[string]kubeObservations)}
		acquisition := &kubeAcquisition{kubeBackend: backend}
		now := time.Now()
		const token = "holder"
		holder := kubeHolder{Duration: 30, Heartbeat: 1}
		read := func(name string, h kubeHolder, at time.Time) bool {
			holders := map[string]kubeHolder{token: h}
			Expect(acquisition.prune(&api.Lease{ObjectMeta: metav1.ObjectMeta{Name: name}}, holders, at)).To(Succeed())
			_, present := holders[token]
			return present
		}
		Expect(read("contended", holder, now)).To(BeTrue())
		for i := range 8193 {
			Expect(read(strconv.Itoa(i), holder, now)).To(BeTrue())
		}
		Expect(read("contended", holder, now.Add(90*time.Second))).To(BeFalse())
		holder.Heartbeat++
		Expect(read("contended", holder, now.Add(90*time.Second))).To(BeTrue())
		Expect(read("contended", holder, now.Add(119*time.Second))).To(BeTrue())
		Expect(read("contended", holder, now.Add(120*time.Second))).To(BeFalse())
	})

	It("reserves enough annotation capacity for newly admitted heartbeats", func(ctx SpecContext) {
		const padding = "padding"
		const token = "holder"
		const name = "capacity"
		future, err := json.Marshal(map[string]kubeHolder{token: {Heartbeat: ^uint64(0), Duration: 30}})
		Expect(err).NotTo(HaveOccurred())
		room := validation.TotalAnnotationSizeLimitB - len(leaseAnnotation) - len(future) - len(padding)
		obj := &api.Lease{ObjectMeta: metav1.ObjectMeta{
			Name: name, Namespace: "test", Annotations: map[string]string{leaseAnnotation: "{}", padding: strings.Repeat("x", room+1)},
		}}
		client := fake.NewClientset(obj).CoordinationV1().Leases(obj.Namespace)
		backend := &kubeBackend{client: client, ttl: 30, observations: make(map[string]kubeObservations)}
		acquisition := &kubeAcquisition{kubeBackend: backend, name: name, token: token}
		_, err = acquisition.try(ctx)
		Expect(err).To(MatchError(ContainSubstring("heartbeat capacity")))

		obj.Annotations[padding] = obj.Annotations[padding][:room]
		_, err = client.Update(ctx, obj, metav1.UpdateOptions{})
		Expect(err).NotTo(HaveOccurred())
		held, err := acquisition.try(ctx)
		Expect(err).NotTo(HaveOccurred())
		for range 10 {
			Expect(held.renew(ctx)).To(Succeed())
		}
		stored, err := client.Get(ctx, name, metav1.GetOptions{})
		Expect(err).NotTo(HaveOccurred())
		Expect(validation.ValidateAnnotationsSize(stored.Annotations)).To(Succeed())
	})

	It("preserves a holder whose heartbeat renews during reclamation", func(ctx SpecContext) {
		const token = "holder"
		holders := map[string]kubeHolder{token: {Duration: 30, Heartbeat: 1}}
		data, err := json.Marshal(holders)
		Expect(err).NotTo(HaveOccurred())
		obj := &api.Lease{ObjectMeta: metav1.ObjectMeta{
			Name: "contended", Namespace: "test", ResourceVersion: "1", Annotations: map[string]string{leaseAnnotation: string(data)},
		}}
		client := fake.NewClientset(obj)
		backend := &kubeBackend{client: client.CoordinationV1().Leases(obj.Namespace), ttl: 30, observations: make(map[string]kubeObservations)}
		attempt := &kubeAcquisition{kubeBackend: backend, name: obj.Name, token: "contender"}
		Expect(attempt.prune(obj, holders, time.Now().Add(-time.Minute))).To(Succeed())

		holders[token] = kubeHolder{Duration: 30, Heartbeat: 2}
		data, err = json.Marshal(holders)
		Expect(err).NotTo(HaveOccurred())
		renewed := obj.DeepCopy()
		renewed.ResourceVersion = "2"
		renewed.Annotations[leaseAnnotation] = string(data)
		client.PrependReactor("update", "leases", func(action clienttest.Action) (bool, runtime.Object, error) {
			next := action.(clienttest.UpdateAction).GetObject().(*api.Lease)
			Expect(next.ResourceVersion).To(Equal(obj.ResourceVersion))
			Expect(client.Tracker().Update(action.GetResource(), renewed, renewed.Namespace)).To(Succeed())
			return true, nil, apierrors.NewConflict(action.GetResource().GroupResource(), renewed.Name, nil)
		})
		held, err := attempt.try(ctx)
		Expect(err).To(MatchError(errBusy))
		Expect(held).To(BeNil())
	})
})
