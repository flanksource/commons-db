package git

import (
	"fmt"
	"io/fs"
	"path"
	"strings"
)

func (t *Tree) node(name string) (treeNode, error) {
	if err := t.validatePath(name); err != nil {
		return treeNode{}, err
	}
	if name == "." {
		return treeNode{path: ".", mode: fs.ModeDir | 0o555, sized: true}, nil
	}
	if node, found := t.nodes[name]; found {
		return node, nil
	}
	parent := path.Dir(name)
	if t.complete || t.listed[parent] {
		return treeNode{}, fs.ErrNotExist
	}
	if err := t.listDirectory(parent); err != nil {
		return treeNode{}, err
	}
	if node, found := t.nodes[name]; found {
		return node, nil
	}
	return treeNode{}, fs.ErrNotExist
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

// listDirectory reads one directory's entries with a non-recursive ls-tree;
// a missing directory, or a blob in its place, lists nothing.
func (t *Tree) listDirectory(directory string) error {
	args := []string{"--literal-pathspecs", "ls-tree", "-z", "--full-tree", "--end-of-options", t.info.SHA}
	if directory != "." {
		args = append(args, "--", directory+"/")
	}
	raw, err := runBytes(t.ctx, command{dir: t.dir, env: t.env, args: args})
	if err != nil {
		return err
	}
	if t.listed == nil {
		t.nodes, t.children, t.listed = map[string]treeNode{}, map[string][]string{}, map[string]bool{}
	}
	for _, entry := range strings.Split(string(raw), "\x00") {
		if entry == "" {
			continue
		}
		node, err := parseTreeNode(entry)
		if err != nil {
			return err
		}
		if path.Dir(node.path) != directory {
			return fmt.Errorf("git tree listing of %q returned %q", directory, node.path)
		}
		t.nodes[node.path] = node
		t.children[directory] = append(t.children[directory], node.path)
	}
	sortNames(t.children[directory])
	t.listed[directory] = true
	return nil
}

// sizedNode is name's node with its blob size, read through the object
// stream the first time it is asked for.
func (t *Tree) sizedNode(name string) (fs.FileInfo, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	node, err := t.node(name)
	if err == nil && !node.sized {
		node, err = t.readSize(node)
	}
	if err != nil {
		return nil, &fs.PathError{Op: "stat", Path: name, Err: err}
	}
	return node, nil
}

func (t *Tree) readSize(node treeNode) (treeNode, error) {
	if err := t.openStream(); err != nil {
		return node, err
	}
	blob, err := t.stream.info(node.object)
	if err != nil {
		return node, err
	}
	if blob.Missing {
		return node, fmt.Errorf("source object %s for %q is missing", node.object, node.path)
	}
	node.size, node.sized = blob.Size, true
	t.nodes[node.path] = node
	return node, nil
}

func parseTreeNode(entry string) (treeNode, error) {
	header, name, ok := strings.Cut(entry, "\t")
	fields := strings.Fields(header)
	if !ok || len(fields) != 3 || !fs.ValidPath(name) || name == "." {
		return treeNode{}, fmt.Errorf("%w: Git tree entry %q", ErrUnsupportedSource, entry)
	}
	node := treeNode{path: name, object: fields[2], mode: 0o444}
	if fields[0] == "040000" && fields[1] == "tree" {
		node.mode, node.sized = fs.ModeDir|0o555, true
		return node, nil
	}
	if fields[1] != "blob" {
		return treeNode{}, fmt.Errorf("%w: Git tree blob entry %q", ErrUnsupportedSource, entry)
	}
	switch fields[0] {
	case "100644":
	case "100755":
		node.mode = 0o555
	case "120000":
		node.mode |= fs.ModeSymlink
	default:
		return treeNode{}, fmt.Errorf("%w: Git tree mode %s at %q", ErrUnsupportedSource, fields[0], name)
	}
	return node, nil
}
