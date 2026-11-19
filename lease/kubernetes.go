package lease

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	api "k8s.io/api/coordination/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/cache"
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
	backend := &kubeBackend{client: client, ttl: int32(options.TTL / time.Second), observations: cache.NewLRUExpireCache(8192)}
	return &locker{options.Options, options.TTL, backend.begin}, nil
}

type kubeBackend struct {
	client       coordination.LeaseInterface
	ttl          int32
	observations *cache.LRUExpireCache
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
		if missing && !create {
			return errNotOwned
		}
		if err != nil && !missing {
			return fmt.Errorf("read Kubernetes lease %s: %w", a.name, err)
		}
		if missing {
			obj = &api.Lease{ObjectMeta: metav1.ObjectMeta{Name: a.name, Annotations: map[string]string{}}}
		}
		if err := a.write(ctx, obj, missing, change); err != nil {
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

func (a *kubeAcquisition) write(ctx context.Context, obj *api.Lease, create bool, change func(map[string]kubeHolder) (bool, error)) error {
	holders := make(map[string]kubeHolder)
	if !create {
		if err := json.Unmarshal([]byte(obj.Annotations[leaseAnnotation]), &holders); err != nil || holders == nil {
			return fmt.Errorf("invalid %s state on Kubernetes lease %s", leaseAnnotation, a.name)
		}
		if err := a.prune(obj, holders); err != nil {
			return err
		}
	}
	changed, result := change(holders)
	if !changed {
		return result
	}
	data, err := json.Marshal(holders)
	if err != nil {
		return err
	}
	obj.Annotations[leaseAnnotation] = string(data)
	if create {
		_, err = a.client.Create(ctx, obj, metav1.CreateOptions{})
	} else {
		_, err = a.client.Update(ctx, obj, metav1.UpdateOptions{})
	}
	if err != nil {
		return fmt.Errorf("update Kubernetes lease %s: %w", a.name, err)
	}
	return result
}

func (a *kubeAcquisition) prune(obj *api.Lease, holders map[string]kubeHolder) error {
	now := time.Now()
	for token, h := range holders {
		if token == "" || h.Duration < 3 || h.Duration > 86400 || h.Mode > groupMode || h.Pending && h.Mode != writeMode || h.Heartbeat == 0 || h.Mode == groupMode && h.Group == "" {
			return fmt.Errorf("invalid holder state on Kubernetes lease %s", a.name)
		}
		key := struct {
			uid, name, token string
			heartbeat        uint64
		}{string(obj.UID), obj.Name, token, h.Heartbeat}
		duration := time.Duration(h.Duration) * time.Second
		if first, ok := a.observations.Get(key); !ok {
			a.observations.Add(key, now, duration*2)
		} else if !now.Before(first.(time.Time).Add(duration)) {
			delete(holders, token)
		}
	}
	return nil
}
