package browse

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io/fs"
	"path"
	"slices"
	"strings"
	"time"

	"github.com/flanksource/commons-db/git"
)

func (s *Service) Files(ctx context.Context, request Request) (Files, error) {
	request.Extensions = slices.Clone(request.Extensions)
	_, repo, err := s.open(ctx, request)
	if err != nil {
		return Files{}, err
	}
	ref, err := resolve(ctx, repo, request.Ref)
	if err != nil {
		return Files{}, err
	}
	paths, err := s.cache.Files(ctx, repo, ref.SHA)
	if err != nil {
		return Files{}, err
	}
	for i, extension := range request.Extensions {
		if !strings.HasPrefix(extension, ".") || strings.ContainsAny(extension, "/\\") {
			return Files{}, fmt.Errorf("%w: invalid extension %q", ErrInvalid, extension)
		}
		request.Extensions[i] = strings.ToLower(extension)
	}
	if len(request.Extensions) > 0 {
		paths = slices.DeleteFunc(paths, func(name string) bool { return !slices.Contains(request.Extensions, strings.ToLower(path.Ext(name))) })
	}
	return Files{SHA: ref.SHA, Paths: paths}, nil
}

func (s *Service) ReadFile(ctx context.Context, request Request) ([]byte, error) {
	if !fs.ValidPath(request.Path) || request.Path == "." || strings.Contains(request.Path, "\\") {
		return nil, fmt.Errorf("%w: file path must be relative to the repository", ErrInvalid)
	}
	_, repo, err := s.open(ctx, request)
	if err != nil {
		return nil, err
	}
	if len(request.Ref) != 40 || strings.Trim(request.Ref, "0123456789abcdefABCDEF") != "" {
		return nil, fmt.Errorf("%w: file reads require a full commit SHA", ErrInvalid)
	}
	paths, err := s.cache.Files(ctx, repo, request.Ref)
	if err != nil {
		return nil, err
	}
	if !slices.Contains(paths, request.Path) {
		return nil, fmt.Errorf("%w: %s at %s", git.ErrPathNotFound, request.Path, request.Ref)
	}
	return s.cache.Show(ctx, repo, request.Ref, request.Path)
}

type commitCursor struct {
	SHA    string `json:"sha"`
	Offset int    `json:"offset"`
}

func (s *Service) Commits(ctx context.Context, request Request) (git.Page[Commit], error) {
	if request.Limit < 0 || request.Limit > 100 {
		return git.Page[Commit]{}, fmt.Errorf("%w: commit limit must be between 1 and 100", ErrInvalid)
	}
	if request.Limit == 0 {
		request.Limit = 50
	}
	_, repo, err := s.open(ctx, request)
	if err != nil {
		return git.Page[Commit]{}, err
	}
	cursor := commitCursor{}
	if request.Cursor != "" {
		data, err := base64.RawURLEncoding.DecodeString(request.Cursor)
		if err != nil || json.Unmarshal(data, &cursor) != nil || cursor.Offset < 0 || len(cursor.SHA) != 40 {
			return git.Page[Commit]{}, fmt.Errorf("%w: invalid commit cursor", ErrInvalid)
		}
	} else {
		ref, err := resolve(ctx, repo, request.Ref)
		if err != nil {
			return git.Page[Commit]{}, err
		}
		cursor.SHA = ref.SHA
	}
	commits, err := s.cache.History(ctx, repo, cursor.SHA, git.HistoryOptions{Limit: request.Limit + 1, Offset: cursor.Offset})
	if err != nil {
		return git.Page[Commit]{}, err
	}
	page := git.Page[Commit]{Items: make([]Commit, 0, min(len(commits), request.Limit))}
	if len(commits) > request.Limit {
		cursor.Offset += request.Limit
		data, err := json.Marshal(cursor)
		if err != nil {
			return git.Page[Commit]{}, err
		}
		page.NextCursor = base64.RawURLEncoding.EncodeToString(data)
		commits = commits[:request.Limit]
	}
	for _, commit := range commits {
		page.Items = append(page.Items, Commit{SHA: commit.SHA, Subject: commit.Subject, Author: commit.Author.Name, Date: commit.Date.Format(time.RFC3339)})
	}
	return page, nil
}
