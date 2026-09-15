package query

import (
	"encoding/json"
	"fmt"

	"github.com/flanksource/clicky/formatters"
)

// ClickyPageFormat is the clicky format an interactive table page is rendered
// in: the document a list response serves as application/json+clicky.
const ClickyPageFormat = "clicky-json"

// RenderClickyPage renders rows of p as the interactive table document: the
// profile's RowPresenter, when it has one, restoring each row's rich cells,
// and its columns' filter and sort keys. It is the one rendering both a list
// page and a session's presented rows go through, so a row a session streams
// is the row a page of the same result serves.
func RenderClickyPage(p Profile, rows []Row) (string, error) {
	filterKeys, err := p.ColumnFilterKeys()
	if err != nil {
		return "", err
	}
	sortKeys, err := p.ColumnSortKeys()
	if err != nil {
		return "", err
	}
	return (&Result{
		Profile: p.Name, Rows: rows,
		ColumnFilterKeys: filterKeys, ColumnSortKeys: sortKeys, Presenter: p.Presenter,
	}).Render(p.Columns, ClickyPageFormat)
}

// PresentClickyRows is each of rows as the table RenderClickyPage renders them
// into presents it, in order.
func PresentClickyRows(p Profile, rows []Row) ([]formatters.ClickyRow, error) {
	if len(rows) == 0 {
		return nil, nil
	}
	rendered, err := RenderClickyPage(p, rows)
	if err != nil {
		return nil, err
	}
	var document formatters.ClickyDocument
	if err := json.Unmarshal([]byte(rendered), &document); err != nil {
		return nil, fmt.Errorf("read the rendered table back: %w", err)
	}
	if document.Node.Kind != "table" || len(document.Node.Rows) != len(rows) {
		return nil, fmt.Errorf("rendering %d rows produced a %q node of %d rows", len(rows), document.Node.Kind, len(document.Node.Rows))
	}
	return document.Node.Rows, nil
}
