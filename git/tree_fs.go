package git

import (
	"bytes"
	"io"
	"io/fs"
	"path"
	"slices"
	"strings"
	"time"
)

type treeNode struct {
	path, object string
	mode         fs.FileMode
	size         int64
	sized        bool
}

func (node treeNode) Name() string       { return path.Base(node.path) }
func (node treeNode) Size() int64        { return node.size }
func (node treeNode) Mode() fs.FileMode  { return node.mode }
func (node treeNode) ModTime() time.Time { return time.Time{} }
func (node treeNode) IsDir() bool        { return node.mode.IsDir() }
func (node treeNode) Sys() any           { return nil }

// treeEntry is a listed directory entry; its Info reads the blob size on
// first use, so listing a tree never asks git for sizes.
type treeEntry struct {
	node treeNode
	tree *Tree
}

func (entry treeEntry) Name() string               { return entry.node.Name() }
func (entry treeEntry) IsDir() bool                { return entry.node.IsDir() }
func (entry treeEntry) Type() fs.FileMode          { return entry.node.mode.Type() }
func (entry treeEntry) Info() (fs.FileInfo, error) { return entry.tree.sizedNode(entry.node.path) }

// ObjectID is the Git object ID (blob or tree) of an entry a Tree listed.
func ObjectID(entry fs.DirEntry) (string, bool) {
	listed, ok := entry.(treeEntry)
	return listed.node.object, ok && listed.node.object != ""
}

func (t *Tree) loadNodes() error {
	raw, err := runBytes(t.ctx, command{dir: t.dir, env: t.env, args: []string{"ls-tree", "-r", "-t", "-z", "--full-tree", "--end-of-options", t.info.SHA}})
	if err != nil {
		return err
	}
	nodes := map[string]treeNode{".": {path: ".", mode: fs.ModeDir | 0o555, sized: true}}
	children := map[string][]string{}
	for _, entry := range strings.Split(string(raw), "\x00") {
		if entry == "" {
			continue
		}
		node, err := parseTreeNode(entry)
		if err != nil {
			return err
		}
		for node.path != "." {
			if _, exists := nodes[node.path]; exists {
				break
			}
			nodes[node.path] = node
			parent := path.Dir(node.path)
			children[parent] = append(children[parent], node.path)
			node = treeNode{path: parent, mode: fs.ModeDir | 0o555, sized: true}
		}
	}
	sortChildren(children)
	t.nodes, t.children = nodes, children
	t.complete = true
	return nil
}

func sortChildren(children map[string][]string) {
	for _, names := range children {
		sortNames(names)
	}
}

func sortNames(names []string) {
	slices.SortFunc(names, func(left, right string) int { return strings.Compare(path.Base(left), path.Base(right)) })
}

func (t *Tree) ReadDir(name string) ([]fs.DirEntry, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if err := t.validatePath(name); err != nil {
		return nil, &fs.PathError{Op: "readdir", Path: name, Err: err}
	}
	if len(t.children[name]) == 0 {
		if err := t.requireDirectory(name); err != nil {
			return nil, &fs.PathError{Op: "readdir", Path: name, Err: err}
		}
	}
	entries := make([]fs.DirEntry, len(t.children[name]))
	for index, child := range t.children[name] {
		entries[index] = treeEntry{node: t.nodes[child], tree: t}
	}
	return entries, nil
}

// requireDirectory loads the whole tree unless it is loaded, so a walk costs
// one listing, and checks name is a directory.
func (t *Tree) requireDirectory(name string) error {
	if !t.complete {
		if err := t.loadNodes(); err != nil {
			return err
		}
	}
	node, err := t.node(name)
	if err != nil {
		return err
	}
	if !node.IsDir() {
		return fs.ErrInvalid
	}
	return nil
}

func (t *Tree) Open(name string) (fs.File, error) {
	t.mu.Lock()
	node, err := t.node(name)
	t.mu.Unlock()
	if err != nil {
		return nil, &fs.PathError{Op: "open", Path: name, Err: err}
	}
	if node.IsDir() {
		entries, err := t.ReadDir(name)
		if err != nil {
			return nil, err
		}
		return &treeDirectory{info: node, entries: entries}, nil
	}
	content, err := t.ReadFile(name)
	if err != nil {
		return nil, err
	}
	node.size, node.sized = int64(len(content)), true
	return &treeFile{Reader: bytes.NewReader(content), info: node}, nil
}

type treeFile struct {
	*bytes.Reader
	info treeNode
}

func (file *treeFile) Stat() (fs.FileInfo, error) { return file.info, nil }
func (file *treeFile) Close() error               { return nil }

type treeDirectory struct {
	info    treeNode
	entries []fs.DirEntry
	offset  int
}

func (directory *treeDirectory) Stat() (fs.FileInfo, error) { return directory.info, nil }
func (directory *treeDirectory) Close() error               { return nil }
func (directory *treeDirectory) Read([]byte) (int, error)   { return 0, fs.ErrInvalid }

func (directory *treeDirectory) ReadDir(count int) ([]fs.DirEntry, error) {
	if count > 0 && directory.offset == len(directory.entries) {
		return nil, io.EOF
	}
	end := len(directory.entries)
	if count > 0 {
		end = min(end, directory.offset+count)
	}
	entries := slices.Clone(directory.entries[directory.offset:end])
	directory.offset = end
	return entries, nil
}
