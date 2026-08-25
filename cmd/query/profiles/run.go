package profiles

import (
	"context"
	"fmt"

	"github.com/flanksource/clicky"
	"github.com/flanksource/clicky/api"
	"github.com/flanksource/commons-db/query"
)

// RunFlags are the flags of the profiles `run` action: which rows of a profile
// to read and where to resume from.
//
// They are the CLI's half of the same paging contract the HTTP export serves,
// and are validated against the same profile RowLimits — a page a caller may
// not request over HTTP is not one it may request from a terminal either.
type RunFlags struct {
	Limit  int      `flag:"limit" help:"Rows per page; defaults to the profile's page size"`
	Offset int      `flag:"offset" help:"Skip this many rows before the page (requires a declared order)"`
	Cursor string   `flag:"cursor" help:"Resume after the position a previous page reported (requires a declared order)"`
	All    bool     `flag:"all" help:"Read forward through every page, stopping at the profile's export ceiling"`
	Params []string `flag:"param" help:"Profile filter param as key=value (repeatable)"`
}

func (RunFlags) ClickyActionFlags() {}

// RunResult is one page of a profile, plus everything needed to ask for the
// next one. The paging facts travel with the rows rather than being printed and
// discarded, so `--format json` carries the cursor a script needs to resume.
type RunResult struct {
	Profile string      `json:"profile"`
	Rows    []query.Row `json:"rows"`

	Limit  int `json:"limit,omitempty"`
	Offset int `json:"offset,omitempty"`

	Total      *query.Total `json:"total,omitempty"`
	HasMore    bool         `json:"hasMore,omitempty"`
	NextCursor query.Cursor `json:"nextCursor,omitempty"`

	// Truncated reports that the read stopped short of the whole result — the
	// export ceiling, or a cap the backend applied. A partial answer that says
	// nothing is the failure this whole contract exists to remove.
	Truncated bool `json:"truncated,omitempty"`

	columns []query.ColumnDef
}

// Run reads one page of a profile, or every page under --all.
func (s *Service) Run(ctx context.Context, name string, options RunFlags) (*RunResult, error) {
	store, err := s.store()
	if err != nil {
		return nil, err
	}
	resolved, err := Resolve(ctx, store, name)
	if err != nil {
		return nil, err
	}
	params, err := parseParamValues(options.Params)
	if err != nil {
		return nil, err
	}
	request, err := runExportRequest(resolved.Profile, options)
	if err != nil {
		return nil, err
	}

	queryCtx := s.context().Wrap(ctx)
	response, err := exportRows(queryCtx, resolved.Profile, params, request)
	if err != nil {
		return nil, err
	}
	rows, overflowed, err := collectBounded(response.rows, response.ceiling)
	if err != nil {
		return nil, err
	}

	return &RunResult{
		Profile:    resolved.Profile.Name,
		Rows:       rows,
		Limit:      request.limit,
		Offset:     request.offset,
		Total:      response.total,
		HasMore:    response.hasMore,
		NextCursor: response.next,
		Truncated:  response.truncated || overflowed,
		columns:    resolved.Profile.Columns,
	}, nil
}

// runExportRequest renders the flags as the same request the HTTP export builds,
// so both surfaces page by one set of rules. The format is fixed: the CLI hands
// rows to clicky's formatter, which is what --format already selects.
func runExportRequest(p query.Profile, options RunFlags) (exportRequest, error) {
	limits := p.RowLimits()
	request := exportRequest{
		format:  "clicky-json",
		scope:   "page",
		limit:   limits.PageSize,
		maxRows: limits.MaxExportRows,
	}
	if options.All {
		if options.Limit != 0 || options.Offset != 0 || options.Cursor != "" {
			return request, fmt.Errorf("--all reads every page, so it cannot be combined with --limit, --offset or --cursor")
		}
		request.scope = "all"
		return request, nil
	}
	if options.Limit != 0 {
		if options.Limit <= 0 || options.Limit > limits.MaxPageSize {
			return request, fmt.Errorf("--limit must be between 1 and %d; read more with --all", limits.MaxPageSize)
		}
		request.limit = options.Limit
	}
	if options.Offset < 0 {
		return request, fmt.Errorf("--offset must be zero or greater")
	}
	request.offset = options.Offset
	request.cursor = query.Cursor(options.Cursor)
	if !request.cursor.IsZero() && request.offset != 0 {
		return request, fmt.Errorf("a cursor already says where to resume, so it cannot be combined with --offset")
	}
	return request, nil
}

// Table renders the page through the profile's declared columns.
func (r *RunResult) Table() api.TextTable {
	return (&query.Result{Profile: r.Profile, Rows: r.Rows}).Table(r.columns)
}

// Render formats the page in the given clicky format.
func (r *RunResult) Render(format string) (string, error) {
	return clicky.Format(r.Table(), clicky.FormatOptions{Format: format})
}

// Pretty renders the rows under a line saying exactly what was read and how to
// read the rest. A page that does not say it is a page is the terminal's
// version of a truncated export.
func (r *RunResult) Pretty() api.Text {
	text := api.Text{Content: r.summary()}
	if r.Truncated {
		text.Children = append(text.Children, api.Text{
			Content: "this read stopped short of the whole result; --all reads to the profile's export ceiling\n",
			Style:   "text-yellow-500",
		})
	}
	if !r.NextCursor.IsZero() {
		text.Children = append(text.Children, api.Text{
			Content: fmt.Sprintf("resume with --cursor %s\n", r.NextCursor),
			Style:   "text-gray-500",
		})
	}
	text.Children = append(text.Children, r.Table())
	return text
}

func (r *RunResult) summary() string {
	span := fmt.Sprintf("%s: rows %d-%d", r.Profile, r.Offset+1, r.Offset+len(r.Rows))
	if len(r.Rows) == 0 {
		span = fmt.Sprintf("%s: no rows", r.Profile)
	}
	if r.Total != nil {
		// A total the backend could only bound is rendered as a bound. Printing
		// it as a count would state a number nobody promised.
		if r.Total.Exact {
			span += fmt.Sprintf(" of %d", r.Total.Value)
		} else {
			span += fmt.Sprintf(" of ~%d+", r.Total.Value)
		}
	}
	if r.HasMore {
		span += " (more available)"
	}
	return span + "\n"
}
