package recordresults_test

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/flanksource/clicky/cache"
	clickyvalkey "github.com/flanksource/clicky/valkey"
	"github.com/google/uuid"
	valkeygo "github.com/valkey-io/valkey-go"

	"github.com/flanksource/commons-db/cmd/query/recordresults"
	"github.com/flanksource/commons-db/query"
	"github.com/flanksource/commons-db/recordstore"
	"github.com/flanksource/commons-db/recordstore/kv"
	"github.com/flanksource/commons-db/recordstore/ndjson"
	"github.com/flanksource/commons-db/recordstore/sqlite"
)

const (
	benchmarkKind          = "benchmark_record"
	benchmarkProfile       = "recordstore-benchmark/benchmark_record"
	benchmarkStream        = "benchmark"
	benchmarkTTL           = time.Hour
	benchmarkMaxChunkBytes = 1 << 20
	benchmarkMaxFileBytes  = 2 << 30
	benchmarkValkeyURL     = "RECORDSTORE_BENCH_VALKEY_URL"
)

type benchmarkBackend string

const (
	benchmarkKVMemory benchmarkBackend = "kv-memory"
	benchmarkKVValkey benchmarkBackend = "kv-valkey"
	benchmarkNDJSON   benchmarkBackend = "ndjson"
	benchmarkSQLite   benchmarkBackend = "sqlite"
)

var benchmarkBackends = []benchmarkBackend{
	benchmarkKVMemory,
	benchmarkKVValkey,
	benchmarkNDJSON,
	benchmarkSQLite,
}

type benchmarkTB interface {
	Helper()
	Fatal(args ...any)
	Fatalf(format string, args ...any)
	Skipf(format string, args ...any)
	TempDir() string
}

func registerBenchmarkSchema(tb benchmarkTB, schemas *recordstore.Schemas) {
	tb.Helper()
	columns, err := query.ColumnsFor(reflect.TypeFor[benchmarkRecordShape]())
	if err != nil {
		tb.Fatalf("reflect benchmark record schema: %v", err)
	}
	for index := range columns {
		if columns[index].Name == "at" {
			columns[index].Kind = query.ColumnKindTimestamp
		}
	}
	if err := schemas.Register(benchmarkKind, columns, recordstore.KindOptions{}); err != nil {
		tb.Fatalf("register benchmark record schema: %v", err)
	}
}

type benchmarkSource struct {
	backend       recordstore.Backend
	sqlite        *sqlite.Backend
	storage       func(context.Context) (int64, error)
	storageBasis  string
	storagePrefix string
	close         func() error
}

func (s benchmarkSource) Close(tb benchmarkTB) {
	tb.Helper()
	if err := s.close(); err != nil {
		tb.Fatalf("close benchmark source: %v", err)
	}
}

func openBenchmarkSource(tb benchmarkTB, backend benchmarkBackend, schemas *recordstore.Schemas) benchmarkSource {
	tb.Helper()
	switch backend {
	case benchmarkKVMemory:
		return openBenchmarkKV(tb, cache.NewMemory(), nil, schemas)
	case benchmarkKVValkey:
		return openBenchmarkValkey(tb, schemas)
	case benchmarkNDJSON:
		dir := filepath.Join(tb.TempDir(), "records")
		store, err := ndjson.New(ndjson.Options{
			Dir:         dir,
			Schema:      schemas.Kind,
			MaxBytes:    benchmarkMaxFileBytes,
			KeepStreams: 1,
			TTL:         benchmarkTTL,
		})
		if err != nil {
			tb.Fatalf("open NDJSON benchmark source: %v", err)
		}
		return benchmarkSource{
			backend: store, storage: func(context.Context) (int64, error) { return benchmarkDirectoryBytes(dir) },
			storageBasis: "NDJSON data and metadata file bytes", close: store.Close,
		}
	case benchmarkSQLite:
		path := filepath.Join(tb.TempDir(), "records.sqlite")
		store, err := sqlite.Open(sqlite.Options{
			Path:          path,
			Schema:        schemas.Kind,
			TTL:           benchmarkTTL,
			SweepInterval: benchmarkTTL,
		})
		if err != nil {
			tb.Fatalf("open SQLite benchmark source: %v", err)
		}
		return benchmarkSource{
			backend: store, sqlite: store, storage: func(context.Context) (int64, error) { return benchmarkSQLiteBytes(path) },
			storageBasis: "SQLite database, WAL, and shared-memory file bytes", close: store.Close,
		}
	default:
		tb.Fatalf("unknown benchmark backend %q", backend)
		return benchmarkSource{}
	}
}

func openBenchmarkValkey(tb benchmarkTB, schemas *recordstore.Schemas) benchmarkSource {
	tb.Helper()
	url := os.Getenv(benchmarkValkeyURL)
	if url == "" {
		tb.Skipf("%s is required for the Valkey benchmark", benchmarkValkeyURL)
	}
	options, err := valkeygo.ParseURL(url)
	if err != nil {
		tb.Fatalf("parse Valkey benchmark URL: %v", err)
	}
	options.DisableCache = true
	options.ForceSingleClient = true
	client, err := valkeygo.NewClient(options)
	if err != nil {
		tb.Fatalf("open Valkey benchmark client: %v", err)
	}
	if err := client.Do(context.Background(), client.B().Ping().Build()).Error(); err != nil {
		client.Close()
		tb.Fatalf("ping Valkey benchmark server: %v", err)
	}
	store := clickyvalkey.NewStore(client)
	source := openBenchmarkKV(tb, store, client.Close, schemas)
	prefix := source.storagePrefix
	source.storage = func(ctx context.Context) (int64, error) {
		keys, err := benchmarkKVKeys(ctx, store, prefix)
		if err != nil {
			return 0, err
		}
		commands := make([]valkeygo.Completed, len(keys))
		for index, key := range keys {
			commands[index] = client.B().MemoryUsage().Key(key).Build()
		}
		var bytes int64
		for index, response := range client.DoMulti(ctx, commands...) {
			used, err := response.AsInt64()
			if err != nil {
				return 0, fmt.Errorf("measure Valkey key %q: %w", keys[index], err)
			}
			bytes += used
		}
		return bytes, nil
	}
	source.storageBasis = "Valkey MEMORY USAGE bytes"
	return source
}

func openBenchmarkKV(tb benchmarkTB, store cache.Store, closeClient func(), schemas *recordstore.Schemas) benchmarkSource {

	tb.Helper()
	prefix := "recordstore-benchmark/" + uuid.NewString()
	backend, err := kv.New(kv.Options{
		Store: store, Prefix: prefix, Schema: schemas.Kind, TTL: benchmarkTTL, MaxChunkBytes: benchmarkMaxChunkBytes,
	})
	if err != nil {
		if closeClient != nil {
			closeClient()
		}
		tb.Fatalf("open KV benchmark source: %v", err)
	}
	return benchmarkSource{
		backend: backend,
		storage: func(ctx context.Context) (int64, error) {
			return benchmarkKVLogicalBytes(ctx, store, prefix)
		},
		storageBasis: "serialized KV values, keys, index members, and scores", storagePrefix: prefix,
		close: func() error {
			err := errors.Join(backend.Close(), removeBenchmarkKV(context.Background(), store, prefix))
			if closeClient != nil {
				closeClient()
			}
			return err
		},
	}
}

func measureBenchmarkStorage(ctx context.Context, source benchmarkSource) (int64, string, error) {
	if source.storage == nil || source.storageBasis == "" {
		return 0, "", errors.New("benchmark source has no storage measurement")
	}
	bytes, err := source.storage(ctx)
	if err != nil {
		return 0, "", err
	}
	if bytes <= 0 {
		return 0, "", fmt.Errorf("benchmark source reported invalid storage size %d", bytes)
	}
	return bytes, source.storageBasis, nil
}

func compressionRatio(inputBytes, storedBytes int64) float64 {
	if inputBytes <= 0 || storedBytes <= 0 {
		panic(fmt.Sprintf("compression ratio requires positive sizes, got input=%d stored=%d", inputBytes, storedBytes))
	}
	return float64(inputBytes) / float64(storedBytes)
}

func benchmarkKVKeys(ctx context.Context, store cache.Store, prefix string) ([]string, error) {
	base := prefix + "/" + benchmarkStream + "/"
	chunks, err := store.ZRangeByScore(ctx, base+"index", cache.NegInf, cache.PosInf)
	if err != nil {
		return nil, fmt.Errorf("list benchmark storage chunks: %w", err)
	}
	keys := []string{base + "meta", base + "index", base + "appended"}
	for _, chunk := range chunks {
		keys = append(keys, base+"chunk/"+chunk)
	}
	return keys, nil
}

func benchmarkKVLogicalBytes(ctx context.Context, store cache.Store, prefix string) (int64, error) {
	keys, err := benchmarkKVKeys(ctx, store, prefix)
	if err != nil {
		return 0, err
	}
	var bytes int64
	for _, key := range keys {
		bytes += int64(len(key))
	}
	for _, key := range append([]string{keys[0]}, keys[3:]...) {
		value, err := store.Get(ctx, key)
		if err != nil {
			return 0, fmt.Errorf("read benchmark storage key %q: %w", key, err)
		}
		bytes += int64(len(value))
	}
	for _, key := range keys[1:3] {
		members, err := store.ZRangeByScore(ctx, key, cache.NegInf, cache.PosInf)
		if err != nil {
			return 0, fmt.Errorf("read benchmark storage index %q: %w", key, err)
		}
		for _, member := range members {
			bytes += int64(len(member)) + 8
		}
	}
	return bytes, nil
}

func benchmarkDirectoryBytes(root string) (int64, error) {
	var bytes int64
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.Type().IsRegular() {
			info, err := entry.Info()
			if err != nil {
				return fmt.Errorf("inspect benchmark file %q: %w", path, err)
			}
			bytes += info.Size()
		}
		return nil
	})
	return bytes, err
}

func benchmarkSQLiteBytes(path string) (int64, error) {
	var bytes int64
	for _, candidate := range []string{path, path + "-wal", path + "-shm"} {
		info, err := os.Stat(candidate)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return 0, fmt.Errorf("inspect benchmark SQLite file %q: %w", candidate, err)
		}
		bytes += info.Size()
	}
	return bytes, nil
}

func removeBenchmarkKV(ctx context.Context, store cache.Store, prefix string) error {
	base := prefix + "/" + benchmarkStream + "/"
	chunks, err := store.ZRangeByScore(ctx, base+"index", cache.NegInf, cache.PosInf)
	if err != nil {
		return fmt.Errorf("list benchmark chunks: %w", err)
	}
	var errs []error
	for _, chunk := range chunks {
		if err := store.Del(ctx, base+"chunk/"+chunk); err != nil {
			errs = append(errs, fmt.Errorf("remove benchmark chunk %s: %w", chunk, err))
		}
	}
	for _, suffix := range []string{"meta", "index", "appended"} {
		if err := store.Del(ctx, base+suffix); err != nil {
			errs = append(errs, fmt.Errorf("remove benchmark %s: %w", suffix, err))
		}
	}
	return errors.Join(errs...)
}

type benchmarkQuery struct {
	registry *recordresults.Registry
	profile  query.Profile
	index    *sqlite.Backend
	close    bool
}

func openBenchmarkQuery(tb testing.TB, source benchmarkSource, schemas *recordstore.Schemas) benchmarkQuery {
	tb.Helper()
	index := source.sqlite
	closeIndex := false
	if index == nil {
		var err error
		index, err = sqlite.Open(sqlite.Options{
			Path:          filepath.Join(tb.TempDir(), "index.sqlite"),
			Schema:        schemas.Kind,
			Derived:       true,
			SweepInterval: benchmarkTTL,
		})
		if err != nil {
			tb.Fatalf("open benchmark query index: %v", err)
		}
		closeIndex = true
	}
	registry, err := recordresults.NewRegistry(recordresults.RegistryOptions{
		Prefix: "recordstore-benchmark", Schemas: schemas, Index: index, Source: source.backend, ConnectionName: "index",
	})
	if err != nil {
		if closeIndex {
			_ = index.Close()
		}
		tb.Fatalf("open benchmark result registry: %v", err)
	}
	if err := recordresults.RegisterResultType(registry, recordresults.ResultType[benchmarkRecordShape]{
		Kind: benchmarkKind, Title: "Benchmark records", TimeColumn: "at",
	}); err != nil {
		if closeIndex {
			_ = index.Close()
		}
		tb.Fatalf("register benchmark result type: %v", err)
	}
	profile, err := registry.Get(context.Background(), benchmarkProfile)
	if err != nil {
		if closeIndex {
			_ = index.Close()
		}
		tb.Fatalf("get benchmark result profile: %v", err)
	}
	return benchmarkQuery{registry: registry, profile: profile, index: index, close: closeIndex}
}

func (q benchmarkQuery) Close(tb testing.TB) {
	tb.Helper()
	if q.close {
		if err := q.index.Close(); err != nil {
			tb.Fatalf("close benchmark query index: %v", err)
		}
	}
}
