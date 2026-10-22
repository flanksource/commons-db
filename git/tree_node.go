package git

import (
	"fmt"
	"io/fs"
	"strconv"
	"strings"
)

func (t *Tree) loadNode(name string) (treeNode, error) {
	raw, err := runBytes(t.ctx, command{dir: t.dir, env: t.env, args: []string{"--literal-pathspecs", "ls-tree", "-l", "-z", "--full-tree", "--end-of-options", t.info.SHA, "--", name}})
	if err != nil {
		return treeNode{}, err
	}
	if len(raw) == 0 {
		return treeNode{}, fs.ErrNotExist
	}
	entry, rest, terminated := strings.Cut(string(raw), "\x00")
	if !terminated || rest != "" {
		return treeNode{}, fmt.Errorf("invalid Git tree lookup for %q", name)
	}
	node, err := parseTreeNode(entry)
	if err != nil {
		return treeNode{}, err
	}
	if node.path != name {
		return treeNode{}, fmt.Errorf("git tree lookup for %q returned %q", name, node.path)
	}
	if t.nodes == nil {
		t.nodes = map[string]treeNode{}
	}
	t.nodes[name] = node
	return node, nil
}

func parseTreeNode(entry string) (treeNode, error) {
	header, name, ok := strings.Cut(entry, "\t")
	fields := strings.Fields(header)
	if !ok || len(fields) != 4 || !fs.ValidPath(name) || name == "." {
		return treeNode{}, fmt.Errorf("%w: Git tree entry %q", ErrUnsupportedSource, entry)
	}
	node := treeNode{path: name, object: fields[2], mode: 0o444}
	if fields[0] == "040000" && fields[1] == "tree" && fields[3] == "-" {
		node.mode = fs.ModeDir | 0o555
		return node, nil
	}
	size, err := strconv.ParseInt(fields[3], 10, 64)
	if fields[1] != "blob" || err != nil || size < 0 {
		return treeNode{}, fmt.Errorf("%w: Git tree blob entry %q", ErrUnsupportedSource, entry)
	}
	node.size = size
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
