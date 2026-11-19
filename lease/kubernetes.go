package lease

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"sync"
	"time"

	"github.com/google/uuid"
	api "k8s.io/api/coordination/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/validation"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	coordination "k8s.io/client-go/kubernetes/typed/coordination/v1"
)

const leaseAnnotation = "commons-db.flanksource.com/holders-v1"

// NewKubernetes uses a namespaced coordination/v1 Lease client.
func NewKubernetes(client coordination.LeaseInterface, options ExpiringOptions) (Locker, error) {
	if client == nil {
		return nil, errors.New("a Kubernetes lease client is required")
	}
	options, err := normalizeExpiring(options)
	if err != nil {
		return nil, err
	}
	backend := &kubeBackend{client: client, ttl: int32(options.TTL / time.Second), observations: make(map[string]kubeObservations)}
	return &locker{options.Options, options.TTL, backend.begin}, nil
}

type kubeBackend struct {
	client       coordination.LeaseInterface
	ttl          int32
	mu           sync.Mutex
	observations map[string]kubeObservations
}

type kubeObservations struct {
	uid     types.UID
	holders map[string]kubeObservation
}

type kubeObservation struct {
	heartbeat uint64
	firstSeen time.Time
}

type kubeHolder struct {
	Mode      mode   `json:"mode"`
	Group     string `json:"group,omitempty"`
	Pending   bool   `json:"pending,omitempty"`
	Heartbeat uint64 `json:"heartbeat"`
	Duration  int32  `json:"duration"`
}

type kubeAcquisition struct {
	*kubeBackend
	request
	name, token  string
	waiting      bool
	registered   bool
	needsCleanup bool
}

func (b *kubeBackend) begin(req request, wait bool) acquisition {
	return &kubeAcquisition{kubeBackend: b, request: req, name: "commons-db." + req.key[:32] + "." + req.key[32:], token: uuid.NewString(), waiting: wait && req.mode == writeMode}
}

func (a *kubeAcquisition) try(ctx context.Context) (ownership, error) {
	err := a.edit(ctx, true, func(holders map[string]kubeHolder) (bool, error) {
		current, present := holders[a.token]
		if a.registered && !present {
			return false, errNotOwned
		}
		next := kubeHolder{Mode: a.mode, Group: a.group, Duration: a.ttl, Heartbeat: current.Heartbeat + 1}
		if !a.admitted(holders) {
			if a.waiting {
				next.Pending = true
				holders[a.token] = next
			}
			return a.waiting, errBusy
		}
		holders[a.token] = next
		return true, nil
	})
	if err != nil {
		a.registered = a.waiting && err == errBusy
		a.needsCleanup = a.waiting || err != errBusy
		return nil, err
	}
	a.needsCleanup = false
	return a, nil
}

func (a *kubeAcquisition) admitted(holders map[string]kubeHolder) bool {
	for token, h := range holders {
		if token == a.token {
			continue
		}
		if h.Pending {
			if !a.waiting {
				return false
			}
			continue
		}
		if h.Mode == writeMode || a.mode == writeMode || h.Mode == groupMode && a.mode == groupMode && a.group != h.Group {
			return false
		}
	}
	return true
}

func (a *kubeAcquisition) abandon(ctx context.Context) error {
	if !a.needsCleanup {
		return nil
	}
	return a.unlock(ctx)
}

func (a *kubeAcquisition) unlock(ctx context.Context) error {
	err := a.edit(ctx, false, func(holders map[string]kubeHolder) (bool, error) {
		_, present := holders[a.token]
		delete(holders, a.token)
		return present, nil
	})
	if err == nil || errors.Is(err, errNotOwned) {
		a.mu.Lock()
		seen := a.observations[a.name]
		delete(seen.holders, a.token)
		if len(seen.holders) == 0 {
			delete(a.observations, a.name)
		}
		a.mu.Unlock()
	}
	if errors.Is(err, errNotOwned) {
		return nil
	}
	return err
}

func (a *kubeAcquisition) renew(ctx context.Context) error {
	return a.edit(ctx, false, func(holders map[string]kubeHolder) (bool, error) {
		h, present := holders[a.token]
		if !present || h.Pending {
			return false, errNotOwned
		}
		h.Heartbeat++
		holders[a.token] = h
		return true, nil
	})
}

func (a *kubeAcquisition) edit(ctx context.Context, create bool, change func(map[string]kubeHolder) (bool, error)) error {
	for ctx.Err() == nil {
		obj, err := a.client.Get(ctx, a.name, metav1.GetOptions{})
		missing := apierrors.IsNotFound(err)
		if missing {
			a.mu.Lock()
			delete(a.observations, a.name)
			a.mu.Unlock()
		}
		if missing && !create {
			return errNotOwned
		}
		if err != nil && !missing {
			return fmt.Errorf("read Kubernetes lease %s: %w", a.name, err)
		}
		if missing {
			obj = &api.Lease{ObjectMeta: metav1.ObjectMeta{Name: a.name, Annotations: map[string]string{}}}
		}
		if err := a.write(ctx, obj, missing, create, change); err != nil {
			if apierrors.IsConflict(err) || apierrors.IsAlreadyExists(err) || apierrors.IsNotFound(err) {
				if err := pause(ctx, 10*time.Millisecond); err != nil {
					return err
				}
				continue
			}
			return err
		}
		return nil
	}
	return context.Cause(ctx)
}

func (a *kubeAcquisition) write(ctx context.Context, obj *api.Lease, missing, acquiring bool, change func(map[string]kubeHolder) (bool, error)) error {
	holders := make(map[string]kubeHolder)
	if !missing {
		if err := json.Unmarshal([]byte(obj.Annotations[leaseAnnotation]), &holders); err != nil || holders == nil {
			return fmt.Errorf("invalid %s state on Kubernetes lease %s", leaseAnnotation, a.name)
		}
		if err := a.prune(obj, holders, time.Now()); err != nil {
			return err
		}
	}
	_, present := holders[a.token]
	changed, result := change(holders)
	if !changed {
		return result
	}
	data, err := json.Marshal(holders)
	if err != nil {
		return err
	}
	obj.Annotations[leaseAnnotation] = string(data)
	if acquiring && !present {
		if size := annotationSizeWithHeartbeatCapacity(obj.Annotations, holders); size > validation.TotalAnnotationSizeLimitB {
			return fmt.Errorf("reserve heartbeat capacity on Kubernetes lease %s: need %d annotation bytes, limit is %d", a.name, size, validation.TotalAnnotationSizeLimitB)
		}
	}
	if missing {
		_, err = a.client.Create(ctx, obj, metav1.CreateOptions{})
	} else {
		_, err = a.client.Update(ctx, obj, metav1.UpdateOptions{})
	}
	if err != nil {
		return fmt.Errorf("update Kubernetes lease %s: %w", a.name, err)
	}
	return result
}

func annotationSizeWithHeartbeatCapacity(annotations map[string]string, holders map[string]kubeHolder) int {
	size := 0
	for key, value := range annotations {
		size += len(key) + len(value)
	}
	for _, h := range holders {
		size += 20 - len(strconv.FormatUint(h.Heartbeat, 10))
	}
	return size
}

func (a *kubeAcquisition) prune(obj *api.Lease, holders map[string]kubeHolder, now time.Time) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	seen := a.observations[obj.Name]
	if seen.uid != obj.UID || seen.holders == nil {
		seen = kubeObservations{uid: obj.UID, holders: make(map[string]kubeObservation)}
	}
	for token := range seen.holders {
		if _, present := holders[token]; !present {
			delete(seen.holders, token)
		}
	}
	if len(holders) == 0 {
		delete(a.observations, obj.Name)
		return nil
	}
	a.observations[obj.Name] = seen
	for token, h := range holders {
		if token == "" || h.Duration < 3 || h.Duration > 86400 || h.Mode > groupMode || h.Pending && h.Mode != writeMode || h.Heartbeat == 0 || h.Mode == groupMode && h.Group == "" {
			return fmt.Errorf("invalid holder state on Kubernetes lease %s", a.name)
		}
		first := seen.holders[token]
		duration := time.Duration(h.Duration) * time.Second
		if h.Heartbeat > first.heartbeat {
			seen.holders[token] = kubeObservation{heartbeat: h.Heartbeat, firstSeen: now}
		} else if h.Heartbeat == first.heartbeat && !now.Before(first.firstSeen.Add(duration)) {
			delete(holders, token)
		}
	}
	return nil
}
