package lease

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	api "k8s.io/api/coordination/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	apivalidation "k8s.io/apimachinery/pkg/api/validation"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/validation"
	coordination "k8s.io/client-go/kubernetes/typed/coordination/v1"
)

const (
	leaseAnnotation = "commons-db.flanksource.com/holders-v1"
	// leaseResourceAnnotation records the exact "<scope>:<key>" a Lease
	// serves, because its DNS name folds case and separators.
	leaseResourceAnnotation = "commons-db.flanksource.com/resource"
)

// NewKubernetes uses a namespaced coordination/v1 Lease client.
func NewKubernetes(client coordination.LeaseInterface, options ExpiringOptions) (Locker, error) {
	if client == nil {
		return nil, errors.New("a Kubernetes lease client is required")
	}
	options, err := normalizeExpiring(options)
	if err != nil {
		return nil, err
	}
	if _, err := kubeName(request{scope: options.Scope, key: "k"}); err != nil {
		return nil, err
	}
	backend := &kubeBackend{client: client, ttl: int32(options.TTL / time.Second), observations: make(map[string]kubeObservations)}
	return &locker{options: options.Options, ttl: options.TTL, begin: backend.begin, holders: backend.holders}, nil
}

// kubeName is "<scope>-<key>" lowercased with "_" as "-", then the first 10
// hex characters of the exact scope and key's digest, so inputs that differ
// only in case or separator get distinct Leases.
func kubeName(req request) (string, error) {
	readable := strings.ToLower(strings.ReplaceAll(req.scope+"-"+req.key, "_", "-"))
	name := readable + "-" + digest(req.scope, req.key)[:10]
	if problems := validation.IsDNS1123Subdomain(name); len(problems) > 0 {
		return "", fmt.Errorf("invalid Kubernetes lease name %q for %s: %s", name, req.resource(), strings.Join(problems, "; "))
	}
	return name, nil
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
	Mode      mode      `json:"mode"`
	Group     string    `json:"group,omitempty"`
	Name      string    `json:"name"`
	Since     time.Time `json:"since"`
	Pending   bool      `json:"pending,omitempty"`
	Heartbeat uint64    `json:"heartbeat"`
	Duration  int32     `json:"duration"`
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
	name, err := kubeName(req)
	if err != nil {
		return refusedAcquisition{err}
	}
	return &kubeAcquisition{kubeBackend: b, request: req, name: name, token: uuid.NewString(), waiting: wait && req.mode == writeMode}
}

func (b *kubeBackend) holders(ctx context.Context, req request) ([]Holder, error) {
	name, err := kubeName(req)
	if err != nil {
		return nil, err
	}
	obj, err := b.client.Get(ctx, name, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read Kubernetes lease %s: %w", name, err)
	}
	holders, err := b.read(obj, req.resource())
	if err != nil {
		return nil, err
	}
	var result []Holder
	for _, h := range holders {
		if !h.Pending {
			result = append(result, Holder{Name: h.Name, Mode: h.Mode.String(), Group: h.Group, Since: h.Since})
		}
	}
	return result, nil
}

func (a *kubeAcquisition) try(ctx context.Context) (ownership, error) {
	err := a.edit(ctx, true, func(holders map[string]kubeHolder) (bool, error) {
		current, present := holders[a.token]
		if a.registered && !present {
			return false, errNotOwned
		}
		next := kubeHolder{Mode: a.mode, Group: a.group, Name: a.holder, Since: time.Now().UTC(), Duration: a.ttl, Heartbeat: current.Heartbeat + 1}
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
			obj = &api.Lease{ObjectMeta: metav1.ObjectMeta{Name: a.name, Annotations: map[string]string{leaseResourceAnnotation: a.resource()}}}
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
		var err error
		if holders, err = a.read(obj, a.resource()); err != nil {
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
		if size := annotationSizeWithHeartbeatCapacity(obj.Annotations, holders); size > apivalidation.TotalAnnotationSizeLimitB {
			return fmt.Errorf("reserve heartbeat capacity on Kubernetes lease %s: need %d annotation bytes, limit is %d", a.name, size, apivalidation.TotalAnnotationSizeLimitB)
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

// read decodes a Lease's live holders after checking that it serves resource.
func (b *kubeBackend) read(obj *api.Lease, resource string) (map[string]kubeHolder, error) {
	if owner := obj.Annotations[leaseResourceAnnotation]; owner != resource {
		return nil, fmt.Errorf("lease %s belongs to %q, not %q", obj.Name, owner, resource)
	}
	holders := make(map[string]kubeHolder)
	if err := json.Unmarshal([]byte(obj.Annotations[leaseAnnotation]), &holders); err != nil || holders == nil {
		return nil, fmt.Errorf("invalid %s state on Kubernetes lease %s", leaseAnnotation, obj.Name)
	}
	return holders, b.prune(obj, holders, time.Now())
}

// annotationSizeWithHeartbeatCapacity is the annotation size once every
// holder's heartbeat and since have grown to their widest encodings. The
// holder name is re-encoded byte for byte on renewal, so it is counted at its
// encoded size; since is counted as a full-precision timestamp with a numeric
// offset, so a renewal that re-encodes it can never overflow the limit.
func annotationSizeWithHeartbeatCapacity(annotations map[string]string, holders map[string]kubeHolder) int {
	size := 0
	for key, value := range annotations {
		size += len(key) + len(value)
	}
	for _, h := range holders {
		size += 20 - len(strconv.FormatUint(h.Heartbeat, 10))
		size += len(time.RFC3339Nano) - len(h.Since.Format(time.RFC3339Nano))
	}
	return size
}

func (b *kubeBackend) prune(obj *api.Lease, holders map[string]kubeHolder, now time.Time) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	seen := b.observations[obj.Name]
	if seen.uid != obj.UID || seen.holders == nil {
		seen = kubeObservations{uid: obj.UID, holders: make(map[string]kubeObservation)}
	}
	for token := range seen.holders {
		if _, present := holders[token]; !present {
			delete(seen.holders, token)
		}
	}
	if len(holders) == 0 {
		delete(b.observations, obj.Name)
		return nil
	}
	b.observations[obj.Name] = seen
	for token, h := range holders {
		if token == "" || h.Name == "" || h.Duration < 3 || h.Duration > 86400 || h.Mode > groupMode || h.Pending && h.Mode != writeMode || h.Heartbeat == 0 || h.Mode == groupMode && h.Group == "" {
			return fmt.Errorf("invalid holder state on Kubernetes lease %s", obj.Name)
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
