package db

import (
	"context"
	"fmt"
	"maps"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"

	"github.com/flanksource/commons/logger"
	"github.com/jackc/pgx/v5"
)

// fastTestingDurabilityParameters turn off every wait on the disk. A server
// without them is durable, which is a different server, so they are enforced
// rather than merely recommended.
var fastTestingDurabilityParameters = map[string]string{
	"fsync":              "off",
	"synchronous_commit": "off",
	"full_page_writes":   "off",
}

// exactTestingSettings must match the computed value; every other scaled
// setting is a floor that a larger running value also satisfies.
var exactTestingSettings = map[string]bool{"jit": true, "wal_level": true, "max_wal_senders": true}

// staleServerWarnings records the servers already warned about, so a process
// that reuses one server for every test warns once.
var staleServerWarnings sync.Map

var settingQuantityPattern = regexp.MustCompile(`^(-?[0-9]+(?:\.[0-9]+)?)\s*([a-zA-Z]*)$`)

var settingUnitScale = map[string]float64{
	"": 1, "B": 1, "kB": 1 << 10, "MB": 1 << 20, "GB": 1 << 30, "TB": 1 << 40,
	"us": 0.001, "ms": 1, "s": 1000, "min": 60_000, "h": 3_600_000, "d": 86_400_000,
}

// serverSetting is one pg_settings row: Setting is in Unit, Display is what
// SHOW prints.
type serverSetting struct {
	Setting, Unit, Display string
}

func validateFastTestingSettings(fsync, synchronousCommit, fullPageWrites string) error {
	if fsync != "off" {
		return fmt.Errorf("fast embedded PostgreSQL requires fsync=off, got %q", fsync)
	}
	if synchronousCommit != "off" {
		return fmt.Errorf("fast embedded PostgreSQL requires synchronous_commit=off, got %q", synchronousCommit)
	}
	if fullPageWrites != "off" {
		return fmt.Errorf("fast embedded PostgreSQL requires full_page_writes=off, got %q", fullPageWrites)
	}
	return nil
}

// validateFastTestingServer fails on a durable server and warns, without
// failing, when a reused server runs with lower settings than scaled.
// stopCommand is the pg_ctl invocation that stops the server so the next run
// restarts it with them.
func validateFastTestingServer(ctx context.Context, conn *pgx.Conn, stopCommand string, scaled map[string]string) error {
	names := slices.Concat(slices.Collect(maps.Keys(scaled)), slices.Collect(maps.Keys(fastTestingDurabilityParameters)))
	have, err := readServerSettings(ctx, conn, names)
	if err != nil {
		return err
	}
	if err := validateFastTestingSettings(
		have["fsync"].Setting,
		have["synchronous_commit"].Setting,
		have["full_page_writes"].Setting,
	); err != nil {
		return err
	}
	drift, err := testingSettingDrift(scaled, have)
	if err != nil {
		return err
	}
	if len(drift) == 0 {
		return nil
	}
	if _, warned := staleServerWarnings.LoadOrStore(stopCommand, true); !warned {
		logger.Warnf(
			"embedded PostgreSQL runs with lower settings than this host supports (%s); run `%s` so the next test run restarts it with them",
			strings.Join(drift, "; "), stopCommand,
		)
	}
	return nil
}

func readServerSettings(ctx context.Context, conn *pgx.Conn, names []string) (map[string]serverSetting, error) {
	rows, err := conn.Query(ctx,
		"SELECT name, setting, coalesce(unit, ''), current_setting(name) FROM pg_settings WHERE name = ANY($1)",
		names,
	)
	if err != nil {
		return nil, fmt.Errorf("read PostgreSQL settings %v: %w", names, err)
	}
	defer rows.Close()
	settings := make(map[string]serverSetting, len(names))
	for rows.Next() {
		var name string
		var setting serverSetting
		if err := rows.Scan(&name, &setting.Setting, &setting.Unit, &setting.Display); err != nil {
			return nil, fmt.Errorf("scan PostgreSQL setting: %w", err)
		}
		settings[name] = setting
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read PostgreSQL settings %v: %w", names, err)
	}
	for _, name := range names {
		if _, ok := settings[name]; !ok {
			return nil, fmt.Errorf("PostgreSQL does not report setting %s", name)
		}
	}
	return settings, nil
}

// testingSettingDrift lists, sorted by name, each setting in want that have
// falls short of.
func testingSettingDrift(want map[string]string, have map[string]serverSetting) ([]string, error) {
	var drift []string
	for _, name := range slices.Sorted(maps.Keys(want)) {
		current, ok := have[name]
		if !ok {
			return nil, fmt.Errorf("PostgreSQL does not report setting %s", name)
		}
		short, err := settingFallsShort(name, want[name], current)
		if err != nil {
			return nil, err
		}
		if short {
			drift = append(drift, fmt.Sprintf("%s: have %s, want %s", name, current.Display, want[name]))
		}
	}
	return drift, nil
}

func settingFallsShort(name, want string, have serverSetting) (bool, error) {
	if exactTestingSettings[name] {
		return have.Setting != want, nil
	}
	wanted, err := settingQuantity(want)
	if err != nil {
		return false, fmt.Errorf("parse computed %s: %w", name, err)
	}
	unit, err := settingQuantity("1" + have.Unit)
	if err != nil {
		return false, fmt.Errorf("parse unit of %s: %w", name, err)
	}
	current, err := strconv.ParseFloat(have.Setting, 64)
	if err != nil {
		return false, fmt.Errorf("parse %s %q: %w", name, have.Setting, err)
	}
	return current*unit < wanted, nil
}

// settingQuantity converts a PostgreSQL setting such as "64MB" or "1d" to bytes
// or milliseconds; a plain number is returned unchanged.
func settingQuantity(value string) (float64, error) {
	match := settingQuantityPattern.FindStringSubmatch(strings.TrimSpace(value))
	if match == nil {
		return 0, fmt.Errorf("unrecognised setting value %q", value)
	}
	scale, ok := settingUnitScale[match[2]]
	if !ok {
		return 0, fmt.Errorf("unrecognised setting unit %q in %q", match[2], value)
	}
	number, err := strconv.ParseFloat(match[1], 64)
	if err != nil {
		return 0, fmt.Errorf("parse setting value %q: %w", value, err)
	}
	return number * scale, nil
}
