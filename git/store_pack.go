package git

import (
	"bufio"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"
)

// PackStore keeps an append-only set of git bundles per repository, written
// without any file lock, so it suits NFS (EFS):
//
//	<Path>/<key>/packs/<unix nanos>-<random>.bundle   one per publish
//	<Path>/<key>/refs.json                            the published refs
//
// Every file is written to a dot-prefixed temporary name in the same
// directory and renamed into place, so a reader never sees a partial file and
// concurrent publishers never overwrite each other's bundles. refs.json is the
// publishers' index of what the store already holds (each bundle carries its
// own refs): a lost concurrent update only makes the next bundle larger.
// Compact folds a key's bundles into one once it holds more than
// CompactAbove; Cache.Maintain calls it.
type PackStore struct {
	Path         string
	CompactAbove int
}

var _ Store = (*PackStore)(nil)

// appliedBundles, inside a local mirror, lists the bundles hydrated into it.
const appliedBundles = "commons-store-bundles"

var errBundleRetired = errors.New("git store bundle was retired during compaction")

type packManifest struct {
	Refs map[string]string `json:"refs"`
}

func (s *PackStore) packs(key string) (string, error) {
	dir, err := storeDir(s.Path, key)
	return filepath.Join(dir, "packs"), err
}

// Hydrate fetches every bundle not yet applied to the local mirror.
func (s *PackStore) Hydrate(ctx context.Context, key, mirror string) error {
	packs, err := s.packs(key)
	if err != nil {
		return err
	}
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		names, err := bundleNames(packs)
		if err != nil {
			return err
		}
		applied, err := readApplied(mirror)
		if err != nil {
			return err
		}
		pending := slices.DeleteFunc(names, func(name string) bool { return applied[name] })
		err = applyBundles(ctx, applyOptions{Dir: mirror, Packs: packs, Names: pending,
			Refspec: "+refs/*:" + storeRefPrefix + "*", Record: true})
		if !errors.Is(err, errBundleRetired) {
			return err
		}
	}
}

type applyOptions struct {
	Dir, Packs, Refspec string
	Names               []string
	// Record appends each applied bundle to the mirror's appliedBundles.
	Record bool
}

// applyBundles fetches each bundle whose prerequisites the repository has,
// in passes, so bundles apply whatever order their names (and the clocks of
// the hosts that wrote them) put them in; a pass without progress fails.
func applyBundles(ctx context.Context, o applyOptions) error {
	pending := o.Names
	for len(pending) > 0 {
		var deferred []string
		var reasons []error
		for _, name := range pending {
			bundle := filepath.Join(o.Packs, name)
			if _, err := run(ctx, command{dir: o.Dir, args: []string{"bundle", "verify", "--quiet", bundle}}); err != nil {
				if _, statErr := os.Stat(bundle); errors.Is(statErr, fs.ErrNotExist) {
					return errBundleRetired
				}
				deferred, reasons = append(deferred, name), append(reasons, err)
				continue
			}
			if _, err := run(ctx, command{dir: o.Dir, args: []string{"fetch", "--quiet", "--no-tags", "--end-of-options", bundle, o.Refspec}}); err != nil {
				if _, statErr := os.Stat(bundle); errors.Is(statErr, fs.ErrNotExist) {
					return errBundleRetired
				}
				return err
			}
			if o.Record {
				if err := recordApplied(o.Dir, name); err != nil {
					return err
				}
			}
		}
		if len(deferred) == len(pending) {
			return fmt.Errorf("apply git store bundles %s: %w", strings.Join(deferred, ", "), errors.Join(reasons...))
		}
		pending = deferred
	}
	return nil
}

// Publish bundles the local mirror's refs the store does not hold yet,
// excluding everything reachable from the refs it does (the whole history
// on the first publish).
func (s *PackStore) Publish(ctx context.Context, key, mirror string) error {
	packs, err := s.packs(key)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(packs, 0o755); err != nil {
		return fmt.Errorf("create git store %s: %w", packs, err)
	}
	manifest, err := readManifest(filepath.Dir(packs))
	if err != nil {
		return err
	}
	local, err := mirrorRefs(ctx, mirror)
	if err != nil {
		return err
	}
	revs, err := bundleRevs(ctx, mirror, local, manifest.Refs)
	if err != nil || revs == "" {
		return err
	}
	if err := createBundle(ctx, mirror, packs, revs); err != nil {
		return err
	}
	return mergeManifest(filepath.Dir(packs), local)
}

// bundleRevs is the rev-list input of the next bundle: every local ref the
// store lacks, minus everything the store already holds. Empty when the
// store holds every object those refs reach.
func bundleRevs(ctx context.Context, mirror string, local, stored map[string]string) (string, error) {
	held, err := presentObjects(ctx, mirror, slices.Collect(maps.Values(stored)))
	if err != nil {
		return "", err
	}
	var revs strings.Builder
	for _, ref := range slices.Sorted(maps.Keys(local)) {
		if !held[local[ref]] {
			fmt.Fprintln(&revs, ref)
		}
	}
	if revs.Len() == 0 {
		return "", nil
	}
	for _, sha := range slices.Sorted(maps.Keys(held)) {
		fmt.Fprintln(&revs, "^"+sha)
	}
	// A new commit settles it cheaply; without one, only new tag objects
	// are listed, which is short.
	for _, probe := range [][]string{{"rev-list", "--max-count=1", "--stdin"}, {"rev-list", "--objects", "--stdin"}} {
		out, err := run(ctx, command{dir: mirror, args: probe, stdin: []byte(revs.String())})
		if err != nil {
			return "", err
		}
		if out != "" {
			return revs.String(), nil
		}
	}
	return "", nil
}

// createBundle writes a bundle of the repository's revs (rev-list --stdin
// input; "" bundles every ref) into packs under a fresh name.
func createBundle(ctx context.Context, repo, packs, revs string) error {
	name, err := newBundleName()
	if err != nil {
		return err
	}
	temporary := filepath.Join(packs, "."+name+".tmp")
	args := []string{"bundle", "create", "--quiet", temporary, "--stdin"}
	if revs == "" {
		args[len(args)-1] = "--all"
	}
	if _, err := run(ctx, command{dir: repo, args: args, stdin: []byte(revs)}); err != nil {
		return errors.Join(err, removeIfExists(temporary))
	}
	if err := os.Rename(temporary, filepath.Join(packs, name)); err != nil {
		return fmt.Errorf("publish git store bundle %s: %w", name, errors.Join(err, removeIfExists(temporary)))
	}
	return nil
}

// Compact folds the key's bundles into one full bundle once there are more
// than CompactAbove, reporting whether it did.
func (s *PackStore) Compact(ctx context.Context, key string) (compacted bool, err error) {
	if s.CompactAbove <= 0 {
		return false, fmt.Errorf("compact git store: CompactAbove must be positive, got %d", s.CompactAbove)
	}
	packs, err := s.packs(key)
	if err != nil {
		return false, err
	}
	names, err := bundleNames(packs)
	if err != nil || len(names) <= s.CompactAbove {
		return false, err
	}
	scratch, err := os.MkdirTemp("", "git-store-compact-")
	if err != nil {
		return false, fmt.Errorf("compact git store %s: %w", key, err)
	}
	defer func() { err = errors.Join(err, os.RemoveAll(scratch)) }()
	if err := ensureMirror(ctx, scratch); err != nil {
		return false, err
	}
	// No detached auto-maintenance may still be writing when scratch is removed.
	if _, err := run(ctx, command{dir: scratch, args: []string{"config", "maintenance.auto", "false"}}); err != nil {
		return false, err
	}
	if err := applyBundles(ctx, applyOptions{Dir: scratch, Packs: packs, Names: names, Refspec: "+refs/*:refs/*"}); err != nil {
		if errors.Is(err, errBundleRetired) {
			return false, nil
		}
		return false, err
	}
	if err := createBundle(ctx, scratch, packs, ""); err != nil {
		return false, err
	}
	for _, name := range names {
		if err := removeIfExists(filepath.Join(packs, name)); err != nil {
			return false, fmt.Errorf("remove compacted bundle %s: %w", name, err)
		}
	}
	return true, nil
}

// Inventory describes every key holding bundles.
func (s *PackStore) Inventory(ctx context.Context) ([]StoreEntry, error) {
	keys, err := storeKeys(s.Path)
	if err != nil {
		return nil, err
	}
	entries := []StoreEntry{}
	for _, key := range keys {
		packs := filepath.Join(s.Path, cacheLayout, key, "packs")
		names, err := bundleNames(packs)
		if err != nil {
			return nil, err
		}
		if len(names) == 0 {
			continue
		}
		entry := StoreEntry{Key: key, Bundles: len(names)}
		for _, name := range names {
			info, err := os.Stat(filepath.Join(packs, name))
			if err != nil {
				return nil, fmt.Errorf("git store bundle %s: %w", name, err)
			}
			entry.SizeBytes += info.Size()
			if at := info.ModTime(); entry.Published == nil || at.After(*entry.Published) {
				entry.Published = &at
			}
		}
		entries = append(entries, entry)
	}
	return entries, nil
}

// bundleNames lists the published bundles, sorted; temporaries are skipped
// and a missing directory has none.
func bundleNames(packs string) ([]string, error) {
	entries, err := os.ReadDir(packs)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("list git store %s: %w", packs, err)
	}
	var names []string
	for _, entry := range entries {
		if name := entry.Name(); entry.Type().IsRegular() && !strings.HasPrefix(name, ".") && strings.HasSuffix(name, ".bundle") {
			names = append(names, name)
		}
	}
	return names, nil
}

func newBundleName() (string, error) {
	suffix := make([]byte, 4)
	if _, err := rand.Read(suffix); err != nil {
		return "", fmt.Errorf("generate git store bundle name: %w", err)
	}
	return fmt.Sprintf("%020d-%s.bundle", time.Now().UnixNano(), hex.EncodeToString(suffix)), nil
}

func readApplied(mirror string) (map[string]bool, error) {
	content, err := os.ReadFile(filepath.Join(mirror, appliedBundles))
	if errors.Is(err, fs.ErrNotExist) {
		return map[string]bool{}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read applied git store bundles: %w", err)
	}
	applied := map[string]bool{}
	for _, name := range lines(string(content)) {
		applied[name] = true
	}
	return applied, nil
}

func recordApplied(mirror, name string) error {
	file, err := os.OpenFile(filepath.Join(mirror, appliedBundles), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return fmt.Errorf("record applied git store bundle %s: %w", name, err)
	}
	_, err = fmt.Fprintln(file, name)
	return errors.Join(err, file.Close())
}

func readManifest(dir string) (packManifest, error) {
	manifest := packManifest{Refs: map[string]string{}}
	content, err := os.ReadFile(filepath.Join(dir, "refs.json"))
	if errors.Is(err, fs.ErrNotExist) {
		return manifest, nil
	}
	if err != nil {
		return manifest, fmt.Errorf("read git store manifest: %w", err)
	}
	if err := json.Unmarshal(content, &manifest); err != nil {
		return manifest, fmt.Errorf("parse git store manifest %s: %w", filepath.Join(dir, "refs.json"), err)
	}
	if manifest.Refs == nil {
		manifest.Refs = map[string]string{}
	}
	return manifest, nil
}

// mergeManifest re-reads refs.json just before replacing it, to narrow the
// window in which a concurrent publisher's refs are lost.
func mergeManifest(dir string, refs map[string]string) error {
	manifest, err := readManifest(dir)
	if err != nil {
		return err
	}
	for ref, sha := range refs {
		manifest.Refs[ref] = sha
	}
	content, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return err
	}
	suffix := make([]byte, 4)
	if _, err := rand.Read(suffix); err != nil {
		return err
	}
	temporary := filepath.Join(dir, ".refs-"+hex.EncodeToString(suffix)+".json")
	if err := os.WriteFile(temporary, content, 0o644); err != nil {
		return fmt.Errorf("write git store manifest: %w", err)
	}
	if err := os.Rename(temporary, filepath.Join(dir, "refs.json")); err != nil {
		return fmt.Errorf("write git store manifest: %w", errors.Join(err, removeIfExists(temporary)))
	}
	return nil
}

// mirrorRefs is every ref of the mirror except the ones a hydrate brought in.
func mirrorRefs(ctx context.Context, mirror string) (map[string]string, error) {
	out, err := run(ctx, command{dir: mirror, args: []string{"for-each-ref", "--format=%(objectname) %(refname)"}})
	if err != nil {
		return nil, err
	}
	refs := map[string]string{}
	for _, line := range lines(out) {
		sha, ref, ok := strings.Cut(line, " ")
		if !ok {
			return nil, fmt.Errorf("unexpected for-each-ref line %q", line)
		}
		if !strings.HasPrefix(ref, storeRefPrefix) {
			refs[ref] = sha
		}
	}
	return refs, nil
}

// presentObjects is the subset of shas the repository has.
func presentObjects(ctx context.Context, dir string, shas []string) (map[string]bool, error) {
	present := map[string]bool{}
	if len(shas) == 0 {
		return present, nil
	}
	out, err := run(ctx, command{dir: dir, args: []string{"cat-file", "--batch-check=%(objectname)"},
		stdin: []byte(strings.Join(shas, "\n") + "\n")})
	if err != nil {
		return nil, err
	}
	scanner := bufio.NewScanner(strings.NewReader(out))
	for scanner.Scan() {
		if fields := strings.Fields(scanner.Text()); len(fields) == 1 {
			present[fields[0]] = true
		}
	}
	return present, scanner.Err()
}

func removeIfExists(path string) error {
	if err := os.Remove(path); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	return nil
}
