package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"github.com/flanksource/commons-db/git"
	"github.com/flanksource/commons-db/git/browse"
)

var Operations = []string{"connections", "repos", "refs", "commits", "files", "pipelines", "pipeline-executions"}

type Options struct {
	Authorize func(context.Context) error
	Service   func(context.Context) (*browse.Service, error)
}

type API struct{ options Options }

func New(options Options) (*API, error) {
	if options.Authorize == nil || options.Service == nil {
		return nil, fmt.Errorf("new git HTTP handlers: authorization and service callbacks are required")
	}
	return &API{options: options}, nil
}

func (a *API) Handler(operation string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if err := a.options.Authorize(r.Context()); err != nil {
			writeError(w, http.StatusForbidden, err)
			return
		}
		request, err := parseRequest(r, operation)
		if err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		service, err := a.options.Service(r.Context())
		if err != nil {
			writeError(w, http.StatusInternalServerError, err)
			return
		}
		result, err := execute(r.Context(), service, operation, request)
		if err != nil {
			writeError(w, ErrorStatus(err), err)
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

func parseRequest(r *http.Request, operation string) (browse.Request, error) {
	query := r.URL.Query()
	request := browse.Request{Connection: strings.TrimSpace(query.Get("connection")), Repo: strings.Trim(strings.TrimSpace(query.Get("repo")), "/"),
		Ref: query.Get("ref"), Pipeline: query.Get("pipeline"), Branch: query.Get("branch"), Cursor: query.Get("cursor"), Extensions: query["extension"]}
	if operation != "connections" && request.Connection == "" {
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
	}
	return nil, fmt.Errorf("%w: unknown Git operation %q", browse.ErrInvalid, operation)
}

func ErrorStatus(err error) int {
	switch {
	case errors.Is(err, browse.ErrInvalid):
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
