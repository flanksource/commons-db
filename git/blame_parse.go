package git

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

const uncommittedSHA = "0000000000000000000000000000000000000000"

func parseBlame(raw []byte) ([]BlameLine, error) {
	lines := []BlameLine{}
	remaining := strings.TrimSuffix(string(raw), "\n")
	for remaining != "" {
		header, rest, ok := strings.Cut(remaining, "\n")
		if !ok {
			return nil, fmt.Errorf("blame: incomplete header %q", header)
		}
		line, sha, err := blameHeader(header)
		if err != nil {
			return nil, err
		}
		metadata := map[string]string{}
		for !strings.HasPrefix(rest, "\t") {
			field, next, ok := strings.Cut(rest, "\n")
			if !ok {
				return nil, fmt.Errorf("blame line %d: missing content", line.Line)
			}
			key, value, _ := strings.Cut(field, " ")
			metadata[key], rest = value, next
		}
		line.Content, remaining, _ = strings.Cut(rest[1:], "\n")
		if line.Line != len(lines)+1 {
			return nil, fmt.Errorf("blame: expected line %d, got %d", len(lines)+1, line.Line)
		}
		if err := blameMetadata(&line, sha, metadata); err != nil {
			return nil, err
		}
		lines = append(lines, line)
	}
	return lines, nil
}

func blameHeader(header string) (BlameLine, string, error) {
	parts := strings.Fields(header)
	if (len(parts) != 3 && len(parts) != 4) || !fullSHAPattern.MatchString(parts[0]) {
		return BlameLine{}, "", fmt.Errorf("blame: invalid header %q", header)
	}
	original, err := strconv.Atoi(parts[1])
	if err != nil || original < 1 {
		return BlameLine{}, "", fmt.Errorf("blame: invalid original line in %q", header)
	}
	line, err := strconv.Atoi(parts[2])
	if err != nil || line < 1 {
		return BlameLine{}, "", fmt.Errorf("blame: invalid final line in %q", header)
	}
	return BlameLine{Line: line, OriginalLine: original}, parts[0], nil
}

func blameMetadata(line *BlameLine, sha string, metadata map[string]string) error {
	path, ok := metadata["filename"]
	if !ok || path == "" {
		return fmt.Errorf("blame line %d: missing filename", line.Line)
	}
	if strings.HasPrefix(path, `"`) {
		decoded, err := strconv.Unquote(path)
		if err != nil {
			return fmt.Errorf("blame line %d: filename: %w", line.Line, err)
		}
		path = decoded
	}
	line.Path = path
	if sha == uncommittedSHA {
		return nil
	}
	for _, key := range []string{"author", "author-mail", "author-time", "summary"} {
		if _, ok := metadata[key]; !ok {
			return fmt.Errorf("blame line %d: missing %s", line.Line, key)
		}
	}
	seconds, err := strconv.ParseInt(metadata["author-time"], 10, 64)
	if err != nil {
		return fmt.Errorf("blame line %d: author-time: %w", line.Line, err)
	}
	line.Commit = &BlameCommit{SHA: sha, Subject: metadata["summary"], Date: time.Unix(seconds, 0).UTC(),
		Author: BlameAuthor{Name: metadata["author"], Email: strings.TrimSuffix(strings.TrimPrefix(metadata["author-mail"], "<"), ">")}}
	return nil
}
