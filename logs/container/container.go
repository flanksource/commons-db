package container

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"maps"
	"net"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/flanksource/commons-db/logs"
)

// FollowOptions selects a container and controls the Docker logs request.
type FollowOptions struct {
	Container string
	Host      string
	Runtime   string
	Since     time.Time
	Tail      string
	Follow    bool
	Emit      func(logs.LogLine)
}

// Follow emits lines until the stream ends or the context is cancelled.
func Follow(ctx context.Context, opts FollowOptions) (result error) {
	defer func() {
		if ctx.Err() != nil {
			result = ctx.Err()
		}
	}()
	if opts.Container == "" || opts.Emit == nil {
		return fmt.Errorf("container and emit are required")
	}
	host := os.Getenv("DOCKER_HOST")
	if host == "" {
		host = "unix:///var/run/docker.sock"
	}
	endpoint, err := url.Parse(host)
	if err != nil || endpoint.Scheme != "unix" || endpoint.Path == "" || endpoint.Host != "" {
		return fmt.Errorf("invalid DOCKER_HOST %q: expected unix:///path", host)
	}
	if opts.Host == "" {
		opts.Host, err = os.Hostname()
		if err != nil {
			return fmt.Errorf("resolve host name: %w", err)
		}
	}
	if opts.Runtime == "" {
		opts.Runtime = "docker"
	}
	transport := &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "unix", endpoint.Path)
	}}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport}
	path := "/containers/" + url.PathEscape(opts.Container)
	response, err := request(ctx, client, path+"/json")
	if err != nil {
		return fmt.Errorf("inspect container %q: %w", opts.Container, err)
	}
	var info struct {
		ID     string `json:"Id"`
		Name   string `json:"Name"`
		Config struct {
			Labels map[string]string `json:"Labels"`
			TTY    bool              `json:"Tty"`
		} `json:"Config"`
	}
	err = json.NewDecoder(response.Body).Decode(&info)
	response.Body.Close()
	if err != nil {
		return fmt.Errorf("decode container %q: %w", opts.Container, err)
	}
	if info.ID == "" || info.Name == "" || info.Config.TTY {
		return fmt.Errorf("inspect container %q: missing identity or TTY stream cannot distinguish stdout and stderr", opts.Container)
	}
	labels := maps.Clone(info.Config.Labels)
	if labels == nil {
		labels = make(map[string]string)
	}
	labels["container.id"], labels["container.name"], labels["container.runtime"] = info.ID, strings.TrimPrefix(info.Name, "/"), opts.Runtime
	query := url.Values{"stdout": {"true"}, "stderr": {"true"}, "follow": {strconv.FormatBool(opts.Follow)}}
	if !opts.Since.IsZero() {
		query.Set("since", strconv.FormatInt(opts.Since.Unix(), 10))
	}
	if opts.Tail != "" {
		query.Set("tail", opts.Tail)
	}
	response, err = request(ctx, client, path+"/logs?"+query.Encode())
	if err != nil {
		return fmt.Errorf("read container %q logs: %w", opts.Container, err)
	}
	defer response.Body.Close()
	return readStream(ctx, response.Body, opts, labels)
}

func request(ctx context.Context, client *http.Client, path string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://docker"+path, nil)
	if err != nil {
		return nil, err
	}
	response, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		defer response.Body.Close()
		body, readErr := io.ReadAll(io.LimitReader(response.Body, 4096))
		if readErr != nil {
			return nil, fmt.Errorf("HTTP %s: read response: %w", response.Status, readErr)
		}
		return nil, fmt.Errorf("HTTP %s: %s", response.Status, strings.TrimSpace(string(body)))
	}
	return response, nil
}

const maxLineBytes = 64 * 1024

func readStream(ctx context.Context, reader io.Reader, opts FollowOptions, labels map[string]string) (result error) {
	var pending [3][]byte
	var capped [3]bool
	emit := func(stream byte, message []byte, truncated bool) {
		source := "stdout"
		if stream == 2 {
			source = "stderr"
		}
		lineLabels := maps.Clone(labels)
		if truncated {
			if lineLabels == nil {
				lineLabels = make(map[string]string)
			}
			lineLabels["truncated"] = "true"
		} else {
			message = bytes.TrimSuffix(message, []byte{'\r'})
		}
		opts.Emit(logs.LogLine{Message: string(message), Source: source, Host: opts.Host, Labels: lineLabels, Count: 1})
	}
	defer func() {
		if result == nil || ctx.Err() != nil {
			for stream := byte(1); stream <= 2; stream++ {
				if len(pending[stream]) > 0 {
					emit(stream, pending[stream], false)
				}
			}
		}
		if ctx.Err() != nil {
			result = ctx.Err()
		}
	}()
	buffer := make([]byte, 32*1024)
	for {
		var header [8]byte
		_, err := io.ReadFull(reader, header[:])
		if err == io.EOF {
			break
		}
		if err != nil {
			return fmt.Errorf("read container log header: %w", err)
		}
		stream := header[0]
		if (stream != 1 && stream != 2 && stream != 3) || header[1] != 0 || header[2] != 0 || header[3] != 0 {
			return fmt.Errorf("invalid container log header: %x", header)
		}
		remaining := int64(binary.BigEndian.Uint32(header[4:]))
		if stream == 3 {
			message, err := io.ReadAll(io.LimitReader(reader, min(remaining, maxLineBytes)))
			if err != nil {
				return fmt.Errorf("read container daemon error: %w", err)
			}
			if int64(len(message)) != min(remaining, maxLineBytes) {
				return fmt.Errorf("read container daemon error: %w", io.ErrUnexpectedEOF)
			}
			if remaining > maxLineBytes {
				return fmt.Errorf("container daemon error (message truncated): %s", message)
			}
			return fmt.Errorf("container daemon error: %s", message)
		}
		for remaining > 0 {
			n, readErr := io.ReadFull(reader, buffer[:min(remaining, int64(len(buffer)))])
			remaining -= int64(n)
			chunk := buffer[:n]
			for len(chunk) > 0 {
				index := bytes.IndexByte(chunk, '\n')
				length := len(chunk)
				if index >= 0 {
					length = index
				}
				length = min(length, maxLineBytes-len(pending[stream]))
				pending[stream] = append(pending[stream], chunk[:length]...)
				chunk = chunk[length:]
				if len(pending[stream]) == maxLineBytes {
					emit(stream, pending[stream], true)
					pending[stream] = pending[stream][:0]
					capped[stream] = true
				} else if len(chunk) > 0 && chunk[0] == '\n' {
					if !capped[stream] || len(bytes.TrimSuffix(pending[stream], []byte{'\r'})) > 0 {
						emit(stream, pending[stream], false)
					}
					pending[stream] = pending[stream][:0]
					capped[stream] = false
					chunk = chunk[1:]
				}
			}
			if readErr != nil {
				return fmt.Errorf("read container log frame: %w", readErr)
			}
		}
	}
	return nil
}
