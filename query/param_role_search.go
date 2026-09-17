package query

// ParamRoleSearch assigns a parameter to the table's free-text search box. The
// profile decides what the text matches — which columns, and how — so the
// search runs server-side with the column filters, the time window, the sort,
// every page and every export, rather than over the rows one page happened to
// load.
const ParamRoleSearch ParamRole = "search"
