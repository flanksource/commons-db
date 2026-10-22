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
}

func (node treeNode) Name() string               { return path.Base(node.path) }
func (node treeNode) Size() int64                { return node.size }
func (node treeNode) Mode() fs.FileMode          { return node.mode }
func (node treeNode) ModTime() time.Time         { return time.Time{} }
func (node treeNode) IsDir() bool                { return node.mode.IsDir() }
func (node treeNode) Sys() any                   { return nil }
func (node treeNode) Type() fs.FileMode          { return node.mode.Type() }
func (node treeNode) Info() (fs.FileInfo, error) { return node, nil }

func (t *Tree) node(name string) (treeNode, error) {
	if err := t.validatePath(name); err != nil {
		return treeNode{}, err
	}
	if name == "." {
		return treeNode{path: ".", mode: fs.ModeDir | 0o555}, nil
	}
	if node, found := t.nodes[name]; found {
		return node, nil
	}
	if t.complete {
		return treeNode{}, fs.ErrNotExist
	}
	return t.loadNode(name)
}

func (t *Tree) validatePath(name string) error {
	if t.closed {
		return fs.ErrClosed
	}
	if err := t.ctx.Err(); err != nil {
		return err
	}
	if !fs.ValidPath(name) || strings.ContainsRune(name, 0) {
		return fs.ErrInvalid
	}
	return nil
}

func (t *Tree) loadNodes() error {
	raw, err := runBytes(t.ctx, command{dir: t.dir, env: t.env, args: []string{"ls-tree", "-r", "-l", "-z", "--full-tree", "--end-of-options", t.info.SHA}})
	if err != nil {
		return err
	}
	nodes := map[string]treeNode{".": {path: ".", mode: fs.ModeDir | 0o555}}
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
			node = treeNode{path: parent, mode: fs.ModeDir | 0o555}
		}
	}
	for _, names := range children {
		slices.SortFunc(names, func(left, right string) int { return strings.Compare(path.Base(left), path.Base(right)) })
	}
	t.nodes, t.children = nodes, children
	t.complete = true
	return nil
}

func (t *Tree) ReadDir(name string) ([]fs.DirEntry, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if err := t.validatePath(name); err != nil {
		return nil, &fs.PathError{Op: "readdir", Path: name, Err: err}
	}
	if !t.complete {
		if err := t.loadNodes(); err != nil {
			return nil, &fs.PathError{Op: "readdir", Path: name, Err: err}
		}
	}
	node, err := t.node(name)
	if err != nil {
		return nil, &fs.PathError{Op: "readdir", Path: name, Err: err}
	}
	if !node.IsDir() {
		return nil, &fs.PathError{Op: "readdir", Path: name, Err: fs.ErrInvalid}
	}
	entries := make([]fs.DirEntry, len(t.children[name]))
	for index, child := range t.children[name] {
		entries[index] = t.nodes[child]
	}
	return entries, nil
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
