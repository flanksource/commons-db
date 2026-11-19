package lease

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/flanksource/commons-db/lease/internal/redsync"
	redispool "github.com/flanksource/commons-db/lease/internal/redsync/redis"
	"github.com/flanksource/commons-db/lease/internal/redsync/redis/valkeygo"
	"github.com/google/uuid"
	"github.com/valkey-io/valkey-go"
	"github.com/valkey-io/valkey-go/valkeycompat"
)

// NewRedis uses one authoritative Redis or Valkey primary per key.
func NewRedis(client valkey.Client, options ExpiringOptions) (Locker, error) {
	if client == nil {
		return nil, errors.New("a Redis or Valkey client is required")
	}
	options, err := normalizeExpiring(options)
	if err != nil {
		return nil, err
	}
	backend := newRedisBackend(client, options.TTL)
	return &locker{options: options.Options, ttl: options.TTL, begin: backend.begin, holders: backend.holders}, nil
}

func newRedisBackend(client valkey.Client, ttl time.Duration) *redisBackend {
	return &redisBackend{client: client, pool: valkeygo.NewPool(valkeycompat.NewAdapter(client)), ttl: ttl}
}

// redisKeys are the writer, shared holders, waiting writers and holder names
// of a key, all in one hash slot.
func redisKeys(req request) []string {
	prefix := "commons-db:lease:{" + req.resource() + "}:"
	return []string{prefix + "writer", prefix + "shared", prefix + "waiting", prefix + "holders"}
}

type redisBackend struct {
	client valkey.Client
	pool   redispool.Pool
	ttl    time.Duration
}

type redisResource struct {
	*redisBackend
	keys []string
}

type redisAcquisition struct {
	redisResource
	request
	share, waiter  string
	registered     bool
	uncertainShare bool
	uncertainToken string
}

func (b *redisBackend) begin(req request, wait bool) acquisition {
	a := &redisAcquisition{redisResource: redisResource{b, redisKeys(req)}, request: req}
	if req.mode == writeMode {
		if wait {
			a.waiter = uuid.NewString()
		}
		return a
	}
	a.share = uuid.NewString()
	if req.mode == groupMode {
		a.share = req.group + ":" + a.share
	}
	return a
}

func (a *redisAcquisition) record() (string, error) {
	data, err := json.Marshal(Holder{Name: a.holder, Mode: a.mode.String(), Group: a.group, Since: time.Now().UTC()})
	return string(data), err
}

func (a *redisAcquisition) try(ctx context.Context) (ownership, error) {
	record, err := a.record()
	if err != nil {
		return nil, err
	}
	if a.share == "" {
		return a.tryWriter(ctx, record)
	}
	a.uncertainShare = true
	if err := a.run(ctx, "share", a.share, a.group, record); err != nil {
		a.uncertainShare = err != errBusy
		return nil, err
	}
	a.uncertainShare = false
	return &redisShared{a.redisResource, a.share}, nil
}

func (a *redisAcquisition) tryWriter(ctx context.Context, record string) (ownership, error) {
	pool := redisWriterPool{Pool: a.pool, resource: a.redisResource, waiter: a.waiter, registered: a.registered, record: record}
	token := uuid.NewString()
	writer := redsync.New(pool).NewMutex(a.keys[0], redsync.WithExpiry(a.ttl), redsync.WithTimeoutFactor(1.0/3), redsync.WithGenValueFunc(func() (string, error) { return token, nil }))
	if err := writer.TryLockContext(ctx); err != nil {
		var taken *redsync.ErrTaken
		if errors.As(err, &taken) {
			a.registered = a.waiter != ""
			return nil, errBusy
		}
		a.uncertainToken = token
		return nil, fmt.Errorf("acquire Redis writer: %w", err)
	}
	return redisWriter{a.redisResource, token}, nil
}

func (a *redisAcquisition) abandon(ctx context.Context) error {
	var err error
	if a.uncertainToken != "" {
		err = (redisWriter{a.redisResource, a.uncertainToken}).unlock(ctx)
	}
	if a.waiter != "" {
		err = errors.Join(err, a.run(ctx, "cancel", a.waiter))
	}
	if a.uncertainShare {
		err = errors.Join(err, a.run(ctx, "release", a.share))
	}
	return err
}

// redisWriter renews and releases through the lease script, so the writer
// key and its holder entry change together.
type redisWriter struct {
	redisResource
	token string
}

func (w redisWriter) renew(ctx context.Context) error  { return w.run(ctx, "extend", w.token) }
func (w redisWriter) unlock(ctx context.Context) error { return w.run(ctx, "unwrite", w.token) }

type redisShared struct {
	redisResource
	token string
}

func (h *redisShared) renew(ctx context.Context) error  { return h.run(ctx, "renew", h.token) }
func (h *redisShared) unlock(ctx context.Context) error { return h.run(ctx, "release", h.token) }

func (r redisResource) params(op string, args ...string) []string {
	return append([]string{op, fmt.Sprint(r.ttl.Milliseconds())}, args...)
}

func (r redisResource) run(ctx context.Context, op string, args ...string) error {
	status, err := redisScript.Exec(ctx, r.client, r.keys, r.params(op, args...)).AsInt64()
	if err != nil {
		return fmt.Errorf("run Redis lease operation %s: %w", op, err)
	}
	switch status {
	case 1:
		return nil
	case 0:
		return errBusy
	case -1:
		return errNotOwned
	default:
		return fmt.Errorf("unexpected Redis lease status %d for %s", status, op)
	}
}

func (b *redisBackend) holders(ctx context.Context, req request) ([]Holder, error) {
	resource := redisResource{b, redisKeys(req)}
	records, err := redisScript.Exec(ctx, b.client, resource.keys, resource.params("holders", "")).AsStrSlice()
	if err != nil {
		return nil, fmt.Errorf("run Redis lease operation holders: %w", err)
	}
	holders := make([]Holder, 0, len(records))
	for _, record := range records {
		var holder Holder
		if err := json.Unmarshal([]byte(record), &holder); err != nil || holder.Name == "" {
			return nil, fmt.Errorf("invalid holder record %q in %s", record, resource.keys[3])
		}
		holders = append(holders, holder)
	}
	return holders, nil
}

type redisWriterPool struct {
	redispool.Pool
	resource       redisResource
	waiter, record string
	registered     bool
}

func (p redisWriterPool) Get(ctx context.Context) (redispool.Conn, error) {
	conn, err := p.Pool.Get(ctx)
	if err != nil {
		return nil, err
	}
	return redisWriterConn{Conn: conn, ctx: ctx, pool: p}, nil
}

type redisWriterConn struct {
	redispool.Conn
	ctx  context.Context
	pool redisWriterPool
}

func (c redisWriterConn) SetNX(key, token string, expiry time.Duration) (bool, error) {
	resource := c.pool.resource
	if key != resource.keys[0] || expiry != resource.ttl {
		return false, errors.New("writer key or expiry does not match the Redis lease resource")
	}
	registered := "0"
	if c.pool.registered {
		registered = "1"
	}
	err := resource.run(c.ctx, "write", token, c.pool.waiter, registered, c.pool.record)
	if err == errBusy {
		return false, nil
	}
	return err == nil, err
}
