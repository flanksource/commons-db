package browse

import (
	"context"
	"errors"
	"time"

	"github.com/flanksource/commons-db/git"
	"github.com/flanksource/commons-db/models"
)

var (
	ErrInvalid           = errors.New("invalid git source")
	ErrUnknownConnection = errors.New("unknown git connection")
)

// Options configures New. PinTTL is how long a commit pin survives without a
// read when CachePrune prunes a mirror (prune is refused without one).
type Options struct {
	Catalog     func(context.Context) ([]models.Connection, error)
	Resolve     func(context.Context, string) (models.Connection, error)
	CacheDir    string
	PinTTL      time.Duration
	Client      func(context.Context, string) (*git.Client, models.Connection, error)
	ClientScope func(context.Context) (string, error)
}

type Service struct {
	options  Options
	cache    *git.Cache
	clients  readMemo[openedClient]
	catalogs readMemo[[]git.RepositoryInfo]
	refs     readMemo[git.RemoteRefs]
}

type openedClient struct {
	Client     *git.Client
	Connection models.Connection
}

type Opened struct {
	Client     *git.Client
	Repository *git.Repository
	Name       string
	Info       git.RepositoryInfo
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
