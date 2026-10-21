package browse

import (
	"context"
	"errors"

	"github.com/flanksource/commons-db/git"
	"github.com/flanksource/commons-db/models"
)

var (
	ErrInvalid           = errors.New("invalid git source")
	ErrUnknownConnection = errors.New("unknown git connection")
)

type Options struct {
	Catalog  func(context.Context) ([]models.Connection, error)
	Resolve  func(context.Context, string) (models.Connection, error)
	CacheDir string
}

type Service struct {
	options Options
	cache   *git.Cache
}

type Request struct {
	Connection string   `json:"connection"`
	Repo       string   `json:"repo,omitempty"`
	Ref        string   `json:"ref,omitempty"`
	Path       string   `json:"path,omitempty"`
	Pipeline   string   `json:"pipeline,omitempty"`
	Branch     string   `json:"branch,omitempty"`
	Cursor     string   `json:"cursor,omitempty"`
	Limit      int      `json:"limit,omitempty"`
	Extensions []string `json:"extensions,omitempty"`
}

type Connection struct {
	Name string `json:"name"`
	Type string `json:"type"`
	URL  string `json:"url,omitempty"`
}

type Repository struct {
	Name          string `json:"name"`
	FullName      string `json:"fullName"`
	Namespace     string `json:"namespace,omitempty"`
	CloneURL      string `json:"cloneUrl,omitempty"`
	WebURL        string `json:"webUrl,omitempty"`
	DefaultBranch string `json:"defaultBranch,omitempty"`
	Private       bool   `json:"private,omitempty"`
	Archived      bool   `json:"archived,omitempty"`
}

type Ref struct {
	Name string `json:"name"`
	SHA  string `json:"sha"`
}

type Refs struct {
	Branches []Ref `json:"branches"`
	Tags     []Ref `json:"tags"`
	Head     *Ref  `json:"head"`
}

type Files struct {
	SHA   string   `json:"sha"`
	Paths []string `json:"paths"`
}

type Commit struct {
	SHA     string `json:"sha"`
	Subject string `json:"subject"`
	Author  string `json:"author"`
	Date    string `json:"date"`
}
