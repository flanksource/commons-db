package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strconv"
	"strings"

	"github.com/flanksource/commons-db/git"
	"github.com/flanksource/commons-db/git/browse"
)

// Operations are the read-only GET operations; Actions the POST operations
// that change the git cache.
var (
	Operations = []string{"connections", "repos", "refs", "commits", "files", "pipelines", "pipeline-executions", "cache", "cache-owners"}
	Actions    = []string{"cache-prune", "cache-evict", "cache-rebind"}
)

// Options configures New: Authorize admits Operations, AuthorizeActions
// admits Actions.
type Options struct {
	Authorize        func(context.Context) error
	AuthorizeActions func(context.Context) error
	Service          func(context.Context) (*browse.Service, error)
}

type API struct{ options Options }

func New(options Options) (*API, error) {
	if options.Authorize == nil || options.AuthorizeActions == nil || options.Service == nil {
		return nil, fmt.Errorf("new git HTTP handlers: operation and action authorization and service callbacks are required")
	}
	return &API{options: options}, nil
}

// maxActionBody bounds a cache action's JSON body.
const maxActionBody = 64 << 10

func (a *API) Handler(operation string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		result, status, err := a.serve(w, r, operation)
		if err != nil {
			writeError(w, status, err)
			return
		}
		data, err := json.Marshal(result)
		if err != nil {
			writeError(w, http.StatusInternalServerError, fmt.Errorf("encode Git response: %w", err))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", "no-store")
		_, _ = w.Write(data)
	}
}

// serve authorizes and parses the request before resolving the service, then
// runs the operation or action, returning the HTTP status of a failure.
func (a *API) serve(w http.ResponseWriter, r *http.Request, operation string) (any, int, error) {
	ctx := r.Context()
	action := slices.Contains(Actions, operation)
	authorize := a.options.Authorize
	if action {
		authorize = a.options.AuthorizeActions
	}
	if err := authorize(ctx); err != nil {
		return nil, http.StatusForbidden, err
	}
	var perform func(*browse.Service) (any, error)
	if action {
		cacheAction, err := parseAction(w, r)
		if err != nil {
			return nil, http.StatusBadRequest, err
		}
		perform = func(service *browse.Service) (any, error) { return executeAction(ctx, service, operation, cacheAction) }
	} else {
		request, err := parseRequest(r, operation)
		if err != nil {
			return nil, http.StatusBadRequest, err
		}
		perform = func(service *browse.Service) (any, error) { return execute(ctx, service, operation, request) }
	}
	service, err := a.options.Service(ctx)
	if err != nil {
		return nil, http.StatusInternalServerError, err
	}
	result, err := perform(service)
	if err != nil {
		return nil, ErrorStatus(err), err
	}
	return result, http.StatusOK, nil
}

func parseAction(w http.ResponseWriter, r *http.Request) (browse.CacheAction, error) {
	var action browse.CacheAction
	if r.Method != http.MethodPost {
		return action, fmt.Errorf("git cache actions are POST requests, got %s", r.Method)
	}
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxActionBody))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&action); err != nil {
		return action, fmt.Errorf("decode git cache action: %w", err)
	}
	action.Owner, action.Connection = strings.TrimSpace(action.Owner), strings.TrimSpace(action.Connection)
	action.Repo = strings.Trim(strings.TrimSpace(action.Repo), "/")
	return action, nil
}

func executeAction(ctx context.Context, service *browse.Service, operation string, action browse.CacheAction) (any, error) {
	switch operation {
	case "cache-prune":
		return service.CachePrune(ctx, action)
	case "cache-evict":
		return service.CacheEvict(ctx, action)
	case "cache-rebind":
		if action.Connection == "" {
			return nil, fmt.Errorf("%w: connection is required to rebind", browse.ErrInvalid)
		}
		return service.CacheRebind(ctx, action)
	}
	return nil, fmt.Errorf("%w: unknown Git cache action %q", browse.ErrInvalid, operation)
}

func parseRequest(r *http.Request, operation string) (browse.Request, error) {
	query := r.URL.Query()
	request := browse.Request{Connection: strings.TrimSpace(query.Get("connection")), Repo: strings.Trim(strings.TrimSpace(query.Get("repo")), "/"),
		Ref: query.Get("ref"), Pipeline: query.Get("pipeline"), Branch: query.Get("branch"), Cursor: query.Get("cursor"), Extensions: query["extension"]}
	if operation != "connections" && operation != "cache" && request.Connection == "" {
		return request, fmt.Errorf("connection query parameter is required")
	}
	if limit := query.Get("limit"); limit != "" {
		value, err := strconv.Atoi(limit)
		if err != nil || value < 1 || value > 100 {
			return request, fmt.Errorf("limit must be between 1 and 100")
		}
		request.Limit = value
	}
	if operation == "pipeline-executions" {
		id, err := strconv.ParseInt(request.Pipeline, 10, 64)
		if err != nil || id <= 0 {
			return request, fmt.Errorf("a positive pipeline ID is required")
		}
	}
	return request, nil
}

func execute(ctx context.Context, service *browse.Service, operation string, request browse.Request) (any, error) {
	switch operation {
	case "connections":
		return service.Connections(ctx)
	case "repos":
		return service.Repositories(ctx, request)
	case "refs":
		return service.Refs(ctx, request)
	case "commits":
		return service.Commits(ctx, request)
	case "files":
		return service.Files(ctx, request)
	case "pipelines":
		return service.Pipelines(ctx, request)
	case "pipeline-executions":
		return service.Executions(ctx, request)
	case "cache":
		return service.Cache(ctx)
	case "cache-owners":
		return service.CacheOwners(ctx, request)
	}
	return nil, fmt.Errorf("%w: unknown Git operation %q", browse.ErrInvalid, operation)
}

func ErrorStatus(err error) int {
	switch {
	case errors.Is(err, git.ErrCacheBusy):
		return http.StatusConflict
	case errors.Is(err, browse.ErrInvalid), errors.Is(err, git.ErrCacheEntry), errors.Is(err, git.ErrUnrelatedHistories), errors.Is(err, git.ErrSharedCacheUnsupported):
		return http.StatusBadRequest
	case errors.Is(err, browse.ErrUnknownConnection), errors.Is(err, git.ErrPathNotFound):
		return http.StatusNotFound
	case errors.Is(err, git.ErrPipelinesUnsupported):
		return http.StatusNotImplemented
	}
	return http.StatusBadGateway
}

func writeError(w http.ResponseWriter, status int, err error) {
	w.Header().Set("Cache-Control", "no-store")
	http.Error(w, git.Redact(err.Error()), status)
}
