package git

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
)

type batchBlob struct {
	Object  string
	Size    int64
	Missing bool
}

type objectBatchOptions struct {
	Dir     string
	Env     []string
	Objects []string
	Read    func(int, batchBlob, io.Reader) error
}

func readObjectBatch(ctx context.Context, options objectBatchOptions) (err error) {
	if len(options.Objects) == 0 {
		return nil
	}
	stream, err := openObjectStream(ctx, command{dir: options.Dir, env: options.Env})
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, stream.close()) }()
	for index, object := range options.Objects {
		if err := stream.read(object, func(blob batchBlob, content io.Reader) error { return options.Read(index, blob, content) }); err != nil {
			return err
		}
	}
	return nil
}

func readBatchObject(reader *bufio.Reader, object string, read func(batchBlob, io.Reader) error) error {
	header, err := reader.ReadString(0)
	if err != nil {
		return fmt.Errorf("read Git blob %q: %w", object, err)
	}
	if header == object+" missing\x00" {
		return read(batchBlob{Missing: true}, strings.NewReader(""))
	}
	fields := strings.Fields(strings.TrimSuffix(header, "\x00"))
	if len(fields) != 3 || fields[1] != "blob" {
		return fmt.Errorf("invalid Git blob %q: %q", object, header)
	}
	if !strings.Contains(object, ":") && fields[0] != object {
		return fmt.Errorf("git blob %q returned object %q", object, fields[0])
	}
	size, err := strconv.ParseInt(fields[2], 10, 64)
	if err != nil || size < 0 {
		return fmt.Errorf("invalid Git blob size for %q: %q", object, header)
	}
	content := &io.LimitedReader{R: reader, N: size}
	if err := read(batchBlob{Object: fields[0], Size: size}, content); err != nil {
		return err
	}
	if content.N != 0 {
		return fmt.Errorf("git blob %q has %d unread bytes", object, content.N)
	}
	if ending, err := reader.ReadByte(); err != nil || ending != 0 {
		return fmt.Errorf("invalid Git blob terminator for %q", object)
	}
	return nil
}
