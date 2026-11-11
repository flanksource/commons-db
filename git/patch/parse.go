package patch

import (
	"bytes"
	"context"
	"errors"
	"os"

	"github.com/bluekeyes/go-gitdiff/gitdiff"
)

type File struct{ native *gitdiff.File }

type FileMetadata struct {
	OldName, NewName                            string
	OldMode, NewMode                            os.FileMode
	IsNew, IsDelete, IsRename, IsCopy, IsBinary bool
}

func Parse(ctx context.Context, raw []byte) ([]*File, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	files, _, err := gitdiff.Parse(bytes.NewReader(raw))
	if err != nil {
		return nil, err
	}
	parsed := make([]*File, len(files))
	for index, file := range files {
		parsed[index] = &File{native: file}
	}
	return parsed, ctx.Err()
}

func (f *File) Metadata() FileMetadata {
	return FileMetadata{OldName: f.native.OldName, NewName: f.native.NewName, OldMode: f.native.OldMode, NewMode: f.native.NewMode,
		IsNew: f.native.IsNew, IsDelete: f.native.IsDelete, IsRename: f.native.IsRename, IsCopy: f.native.IsCopy, IsBinary: f.native.IsBinary}
}

func (f *File) String() string { return f.native.String() }

func (f *File) Apply(ctx context.Context, body []byte) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	var out bytes.Buffer
	if err := gitdiff.Apply(&out, bytes.NewReader(body), f.native); err != nil {
		return nil, err
	}
	return out.Bytes(), ctx.Err()
}

func (f *File) Reverse(ctx context.Context) (*File, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if f.native.IsBinary && f.native.ReverseBinaryFragment == nil {
		return nil, errors.New("binary patch has no reverse fragment")
	}
	out := *f.native
	out.OldName, out.NewName = out.NewName, out.OldName
	out.OldMode, out.NewMode = out.NewMode, out.OldMode
	out.OldOIDPrefix, out.NewOIDPrefix = out.NewOIDPrefix, out.OldOIDPrefix
	out.IsNew, out.IsDelete = out.IsDelete, out.IsNew
	out.BinaryFragment, out.ReverseBinaryFragment = out.ReverseBinaryFragment, out.BinaryFragment
	out.TextFragments = make([]*gitdiff.TextFragment, 0, len(f.native.TextFragments))
	for _, fragment := range f.native.TextFragments {
		frag := *fragment
		frag.OldPosition, frag.NewPosition = fragment.NewPosition, fragment.OldPosition
		frag.OldLines, frag.NewLines = fragment.NewLines, fragment.OldLines
		frag.LinesAdded, frag.LinesDeleted = fragment.LinesDeleted, fragment.LinesAdded
		frag.Lines = make([]gitdiff.Line, len(fragment.Lines))
		for index, line := range fragment.Lines {
			switch line.Op {
			case gitdiff.OpAdd:
				line.Op = gitdiff.OpDelete
			case gitdiff.OpDelete:
				line.Op = gitdiff.OpAdd
			}
			frag.Lines[index] = line
		}
		out.TextFragments = append(out.TextFragments, &frag)
	}
	return &File{native: &out}, ctx.Err()
}
