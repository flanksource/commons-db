// Benchmarks how well, and how fast, each candidate codec compresses a JFR
// recording: the testdata fixtures, or the recording JFR_FILE names
// (`make -C tracing jfr-compression`).

package jfr_test

import (
	"bytes"
	"compress/gzip"
	"fmt"
	"io"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/andybalholm/brotli"
	"github.com/klauspost/compress/s2"
	"github.com/klauspost/compress/zstd"
	"github.com/pierrec/lz4/v4"
	"github.com/ulikunitz/xz"
)

type codec struct {
	name       string
	compress   func(io.Writer) (io.WriteCloser, error)
	decompress func(io.Reader) (io.Reader, error)
}

func gzipLevel(level int) codec {
	return codec{
		name:       fmt.Sprintf("gzip-%d", level),
		compress:   func(w io.Writer) (io.WriteCloser, error) { return gzip.NewWriterLevel(w, level) },
		decompress: func(r io.Reader) (io.Reader, error) { return gzip.NewReader(r) },
	}
}

func zstdLevel(name string, level zstd.EncoderLevel) codec {
	return codec{
		name: "zstd-" + name,
		compress: func(w io.Writer) (io.WriteCloser, error) {
			return zstd.NewWriter(w, zstd.WithEncoderLevel(level))
		},
		decompress: func(r io.Reader) (io.Reader, error) { return zstd.NewReader(r) },
	}
}

func brotliLevel(level int) codec {
	return codec{
		name:       fmt.Sprintf("brotli-%d", level),
		compress:   func(w io.Writer) (io.WriteCloser, error) { return brotli.NewWriterLevel(w, level), nil },
		decompress: func(r io.Reader) (io.Reader, error) { return brotli.NewReader(r), nil },
	}
}

var codecs = []codec{
	gzipLevel(gzip.BestSpeed), gzipLevel(6), gzipLevel(gzip.BestCompression),
	zstdLevel("fastest", zstd.SpeedFastest), zstdLevel("default", zstd.SpeedDefault),
	zstdLevel("better", zstd.SpeedBetterCompression), zstdLevel("best", zstd.SpeedBestCompression),
	brotliLevel(5), brotliLevel(11),
	{
		name:       "s2",
		compress:   func(w io.Writer) (io.WriteCloser, error) { return s2.NewWriter(w), nil },
		decompress: func(r io.Reader) (io.Reader, error) { return s2.NewReader(r), nil },
	},
	{
		name:       "lz4",
		compress:   func(w io.Writer) (io.WriteCloser, error) { return lz4.NewWriter(w), nil },
		decompress: func(r io.Reader) (io.Reader, error) { return lz4.NewReader(r), nil },
	},
	{
		name:       "xz",
		compress:   func(w io.Writer) (io.WriteCloser, error) { return xz.NewWriter(w) },
		decompress: func(r io.Reader) (io.Reader, error) { return xz.NewReader(r) },
	},
}

// recordings are the uncompressed recordings to benchmark, by name: the file
// JFR_FILE names, or every testdata fixture.
func recordings(b *testing.B) map[string][]byte {
	paths := []string{os.Getenv("JFR_FILE")}
	if paths[0] == "" {
		var err error
		if paths, err = filepath.Glob(filepath.Join("testdata", "*.jfr.gz")); err != nil || len(paths) == 0 {
			b.Fatalf("no recordings in testdata to benchmark (err=%v)", err)
		}
	}
	recordings := map[string][]byte{}
	for _, path := range paths {
		data, err := os.ReadFile(path)
		if err != nil {
			b.Fatalf("read recording: %v", err)
		}
		if bytes.HasPrefix(data, []byte{0x1f, 0x8b}) {
			data = mustDecompress(b, gzipLevel(6), data)
		}
		if !bytes.HasPrefix(data, []byte("FLR\x00")) {
			b.Fatalf("%s is not a JFR recording: it does not start with the FLR magic", path)
		}
		recordings[strings.TrimSuffix(strings.TrimSuffix(filepath.Base(path), ".gz"), ".jfr")] = data
	}
	return recordings
}

func mustCompress(b *testing.B, c codec, data []byte) []byte {
	var out bytes.Buffer
	w, err := c.compress(&out)
	if err != nil {
		b.Fatalf("%s: open writer: %v", c.name, err)
	}
	if _, err := w.Write(data); err != nil {
		b.Fatalf("%s: compress: %v", c.name, err)
	}
	if err := w.Close(); err != nil {
		b.Fatalf("%s: close writer: %v", c.name, err)
	}
	return out.Bytes()
}

func mustDecompress(b *testing.B, c codec, data []byte) []byte {
	r, err := c.decompress(bytes.NewReader(data))
	if err != nil {
		b.Fatalf("%s: open reader: %v", c.name, err)
	}
	out, err := io.ReadAll(r)
	if err != nil {
		b.Fatalf("%s: decompress: %v", c.name, err)
	}
	return out
}

// BenchmarkCompression reports, per recording and codec, the compressed size
// and ratio alongside compress and decompress throughput over the
// uncompressed bytes. Each codec must round-trip the recording exactly.
func BenchmarkCompression(b *testing.B) {
	all := recordings(b)
	for _, name := range slices.Sorted(maps.Keys(all)) {
		data := all[name]
		for _, c := range codecs {
			compressed := mustCompress(b, c, data)
			if !bytes.Equal(mustDecompress(b, c, compressed), data) {
				b.Fatalf("%s: %s does not round-trip", name, c.name)
			}
			ratio := float64(len(data)) / float64(len(compressed))
			b.Run(name+"/"+c.name+"/compress", func(b *testing.B) {
				b.SetBytes(int64(len(data)))
				for b.Loop() {
					mustCompress(b, c, data)
				}
				b.ReportMetric(float64(len(compressed)), "compressed-bytes")
				b.ReportMetric(ratio, "ratio")
			})
			b.Run(name+"/"+c.name+"/decompress", func(b *testing.B) {
				b.SetBytes(int64(len(data)))
				for b.Loop() {
					mustDecompress(b, c, compressed)
				}
			})
		}
	}
}
