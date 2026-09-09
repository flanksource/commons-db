package schedules

import (
	"bytes"
	"context"
	"fmt"
	"path"
	"strings"

	"github.com/flanksource/commons-db/fs"
)

// fsArtifactStore writes rendered reports through commons-db's own Filesystem
// abstraction, so the same store works against a local directory in development
// and S3, GCS, SFTP or SMB in a deployment without the runner knowing which.
type fsArtifactStore struct {
	filesystem fs.FilesystemRW
	root       string
}

// NewArtifactStore stores reports under root on the given filesystem.
func NewArtifactStore(filesystem fs.FilesystemRW, root string) (ArtifactStore, error) {
	if filesystem == nil {
		return nil, fmt.Errorf("artifact store requires a filesystem")
	}
	return &fsArtifactStore{filesystem: filesystem, root: strings.Trim(root, "/")}, nil
}

// Save writes the artifact and returns it with its stored path filled in.
//
// The path is namespaced by schedule and run so a report is traceable to the run
// that produced it, and two runs of the same schedule never overwrite each
// other's output.
func (s *fsArtifactStore) Save(
	ctx context.Context,
	schedule, runID string,
	artifact Artifact,
) (Artifact, error) {
	filename := artifact.Filename
	if filename == "" {
		filename = "report"
	}
	target := path.Join(s.root, sanitizePathSegment(schedule), sanitizePathSegment(runID), filename)

	if _, err := s.filesystem.Write(ctx, target, bytes.NewReader(artifact.Content)); err != nil {
		return Artifact{}, fmt.Errorf("write artifact %q: %w", target, err)
	}
	artifact.Path = target
	return artifact, nil
}

// sanitizePathSegment keeps a name from escaping its directory. Schedule names
// are author-supplied and may contain dots and slashes, which is fine in a name
// and not fine in a path.
func sanitizePathSegment(value string) string {
	cleaned := strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
			return r
		case r == '-', r == '_':
			return r
		default:
			return '-'
		}
	}, value)
	cleaned = strings.Trim(cleaned, "-")
	if cleaned == "" {
		return "unnamed"
	}
	return cleaned
}
