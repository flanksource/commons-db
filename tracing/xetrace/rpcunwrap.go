package xetrace

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// UnwrapRPC rewrites a SQL Server RPC invocation into the inner statement it
// is actually executing, with parameter placeholders substituted by their
// literal values. Three shapes are recognized:
//
//  1. sp_prepexec — wrapped in a `declare @p1 int set @p1=N exec sp_prepexec @p1
//     output, N'@P0 type', N'TEMPLATE', v0, v1, … select @p1` scaffold.
//  2. sp_executesql — `[exec] sp_executesql N'TEMPLATE', N'@P0 type', v0, v1, …`.
//  3. Positional CALL — `{call proc(?, ?, ?)}` or `call proc(?, ?, ?)` with
//     values supplied as additional ordered args (we don't see those at this
//     layer, so positional CALL is returned untouched with `?` placeholders).
//
// When no rewrite applies the original input is returned verbatim along with
// ok=false, so callers can decide whether to fall back to raw rendering.
func UnwrapRPC(raw string) (unwrapped string, ok bool) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return raw, false
	}

	if stripped, didStrip := stripPrepexecScaffold(trimmed); didStrip {
		if inner, innerOK := parsePrepexec(stripped); innerOK {
			return inner, true
		}
		// Scaffold stripped but body unparseable — surface the body
		// anyway so the user sees the actual call without the wrapping.
		return stripped, true
	}

	if inner, innerOK := parseExecuteSQL(trimmed); innerOK {
		return inner, true
	}

	return raw, false
}

// prepexecScaffoldRe matches the `declare @pN int set @pN=M exec sp_prepexec
// @pN output,` prefix that precedes every sp_prepexec call. Case-insensitive
// (SQL Server emits both `declare` and `DECLARE`). The capture group holds M,
// the prepared-statement handle — see prepexecHandle.
var prepexecScaffoldRe = regexp.MustCompile(`(?is)^\s*declare\s+@p\d+\s+int\s+set\s+@p\d+\s*=\s*(\d+)\s+exec(?:ute)?\s+sp_prepexec\s+@p\d+\s+output\s*,\s*`)

// prepexecHandle returns the prepared-statement handle from an sp_prepexec
// batch. The handle is an OUTPUT parameter, and XE renders RPC parameters as
// they stood on completion, so the captured value is the handle the server
// assigned — the same id a later sp_execute names.
//
// A zero handle (the placeholder a client sends in before the server replies)
// is reported as absent, so it is never cached as if it identified a statement.
func prepexecHandle(s string) (int, bool) {
	m := prepexecScaffoldRe.FindStringSubmatch(s)
	if m == nil {
		return 0, false
	}
	handle, err := strconv.Atoi(m[1])
	if err != nil || handle == 0 {
		return 0, false
	}
	return handle, true
}

// spExecuteRe matches `[exec[ute]] sp_execute <handle>, <values…>` — the RPC a
// driver sends to re-run an already-prepared statement. Unlike sp_prepexec it
// carries NO SQL text: only the handle and the arguments.
var spExecuteRe = regexp.MustCompile(`(?is)^\s*(?:exec(?:ute)?\s+)?sp_execute\s+`)

// parseSPExecute splits an sp_execute call into its handle and raw value
// tokens.
func parseSPExecute(s string) (handle int, values []string, ok bool) {
	loc := spExecuteRe.FindStringIndex(s)
	if loc == nil {
		return 0, nil, false
	}
	args, split := splitTopLevelArgs(strings.TrimSpace(s[loc[1]:]))
	if !split || len(args) == 0 {
		return 0, nil, false
	}
	handle, err := strconv.Atoi(strings.TrimSpace(args[0]))
	if err != nil {
		return 0, nil, false
	}
	return handle, args[1:], true
}

// callEscapeRe matches the JDBC positional call escape, `{call p(?, ?)}` or
// `call p(?, ?)`, which reaches the trace only when the driver did not expand
// it into an RPC. Its argument values are not in the event at all.
var callEscapeRe = regexp.MustCompile(`(?is)^\s*\{?\s*call\s`)

// hasUnboundParams reports whether a display statement still carries
// placeholders instead of literal values — an unexpanded positional call, or an
// @PN the unwrap could not substitute because the event shipped fewer values
// than the template declares. Callers surface this so a placeholder is never
// mistaken for the real call.
//
// String literals are stripped before the `?` check, so `SELECT 'what?'` is not
// mistaken for a placeholder.
func hasUnboundParams(sql string) bool {
	if paramNameRe.MatchString(sql) {
		return true
	}
	if !callEscapeRe.MatchString(sql) {
		return false
	}
	return strings.Contains(stripStringLiterals(sql), "?")
}

// stripStringLiterals removes single-quoted literals so a `?` inside data is
// not read as a bind placeholder.
func stripStringLiterals(s string) string {
	var b strings.Builder
	inLiteral := false
	for i := 0; i < len(s); i++ {
		if s[i] == '\'' {
			if inLiteral && i+1 < len(s) && s[i+1] == '\'' {
				i++
				continue
			}
			inLiteral = !inLiteral
			continue
		}
		if !inLiteral {
			b.WriteByte(s[i])
		}
	}
	return b.String()
}

// HandleCache resolves sp_execute calls back into the statement their
// sp_prepexec prepared. It is stateful across polls by necessity: a handle can
// be prepared in one ring-buffer read and executed in the next.
//
// Not safe for concurrent use — Drain owns one per capture and calls it from a
// single goroutine.
type HandleCache struct {
	prepared map[[2]int]*RPCCall
}

// NewHandleCache returns an empty cache.
func NewHandleCache() *HandleCache {
	return &HandleCache{prepared: map[[2]int]*RPCCall{}}
}

// Observe records the SQL template an sp_prepexec event prepared, keyed by its
// handle. Events that are not sp_prepexec are ignored.
func (c *HandleCache) Observe(e Event) {
	handle, ok := prepexecHandle(collapseWhitespace(strings.TrimSpace(e.Statement)))
	if !ok {
		return
	}
	call, parsed := ParseRPC(e.Statement)
	if !parsed || call.Template == "" {
		return
	}
	// Prepared handle numbers are local to a SQL Server connection.
	c.prepared[[2]int{e.SessionID, handle}] = call
}

// Resolve rewrites an sp_execute event in place into the call it re-ran,
// substituting the event's own argument values into the cached template. When
// the handle is unknown — prepared before capture started — the values are kept
// and the missing text is named explicitly, with ParamsUnavailable set.
//
// Events that are not sp_execute are left alone.
func (c *HandleCache) Resolve(e *Event) {
	stmt := collapseWhitespace(strings.TrimSpace(e.Statement))
	handle, values, ok := parseSPExecute(stmt)
	if !ok {
		return
	}

	call, cached := c.prepared[[2]int{e.SessionID, handle}]
	if !cached {
		display := make([]string, len(values))
		for i, v := range values {
			display[i] = formatArgForDisplay(v)
		}
		e.SQL = fmt.Sprintf("EXEC <%s %d> (%s)", spExecuteMarker, handle, strings.Join(display, ", "))
		deriveFromSQL(e)
		e.ParamsUnavailable = true
		return
	}

	e.SQL = substituteParams(call.Template, call.ParamDecl, values)
	deriveFromSQL(e)
}

// prepexecTrailRe matches the trailing `select @pN` that sp_prepexec batches
// always append so the caller can read the handle.
var prepexecTrailRe = regexp.MustCompile(`(?is)\s+select\s+@p\d+\s*$`)

// stripPrepexecScaffold removes the declare/set/exec prefix and the trailing
// select @pN from a sp_prepexec batch. Returns the trimmed middle plus ok.
func stripPrepexecScaffold(s string) (string, bool) {
	loc := prepexecScaffoldRe.FindStringIndex(s)
	if loc == nil {
		return s, false
	}
	body := s[loc[1]:]
	if tail := prepexecTrailRe.FindStringIndex(body); tail != nil {
		body = body[:tail[0]]
	}
	return strings.TrimSpace(body), true
}

// parsePrepexec parses a scaffold-stripped sp_prepexec body of the form:
//
//	N'@P0 int, @P1 nvarchar(10)', N'TEMPLATE', value0, value1, ...
//
// SQL Server also accepts NULL for "no parameter declarations":
//
//	NULL, N'TEMPLATE', value0, ...
//
// The first arg is the param decl (NULL or string), the second is the SQL
// template, and the rest are positional values matched to @P0/@P1/…
// Whitespace around commas is tolerated.
func parsePrepexec(body string) (string, bool) {
	args, ok := splitTopLevelArgs(body)
	if !ok || len(args) < 2 {
		return "", false
	}
	paramDecl := ""
	if !strings.EqualFold(strings.TrimSpace(args[0]), "NULL") {
		decl, declOK := asStringLiteral(args[0])
		if !declOK {
			return "", false
		}
		paramDecl = decl
	}
	template, ok := asStringLiteral(args[1])
	if !ok {
		return "", false
	}
	values := args[2:]
	return substituteParams(template, paramDecl, values), true
}

// executeSQLRe matches `[exec[ute]] sp_executesql <body>` at the start of a
// statement. Unlike sp_prepexec there is no wrapping declare/select.
var executeSQLRe = regexp.MustCompile(`(?is)^\s*(?:exec(?:ute)?\s+)?sp_executesql\s+`)

func parseExecuteSQL(s string) (string, bool) {
	loc := executeSQLRe.FindStringIndex(s)
	if loc == nil {
		return "", false
	}
	body := strings.TrimSpace(s[loc[1]:])
	args, ok := splitTopLevelArgs(body)
	if !ok || len(args) < 1 {
		return "", false
	}
	template, ok := asStringLiteral(args[0])
	if !ok {
		return "", false
	}
	// Param declaration is optional: `sp_executesql N'SELECT 1'` is valid.
	paramDecl := ""
	var values []string
	if len(args) >= 2 {
		if decl, declOK := asStringLiteral(args[1]); declOK {
			paramDecl = decl
			values = args[2:]
		} else {
			// No decl string — second arg onward are the values.
			values = args[1:]
		}
	}
	return substituteParams(template, paramDecl, values), true
}

// splitTopLevelArgs splits a comma-separated argument list respecting string
// literals (including N'...' with doubled ” escapes) and parenthesis depth.
// Whitespace is preserved inside args but trimmed at their edges.
func splitTopLevelArgs(s string) ([]string, bool) {
	var args []string
	var cur strings.Builder
	depth := 0
	i := 0
	for i < len(s) {
		c := s[i]
		switch {
		case c == '\'' || (c == 'N' && i+1 < len(s) && s[i+1] == '\''):
			// Consume a full string literal, including opening N if present.
			if c == 'N' {
				cur.WriteByte('N')
				i++
				c = s[i]
			}
			cur.WriteByte('\'')
			i++
			for i < len(s) {
				if s[i] == '\'' {
					if i+1 < len(s) && s[i+1] == '\'' {
						cur.WriteString("''")
						i += 2
						continue
					}
					cur.WriteByte('\'')
					i++
					break
				}
				cur.WriteByte(s[i])
				i++
			}
		case c == '(':
			depth++
			cur.WriteByte(c)
			i++
		case c == ')':
			depth--
			cur.WriteByte(c)
			i++
		case c == ',' && depth == 0:
			args = append(args, strings.TrimSpace(cur.String()))
			cur.Reset()
			i++
		default:
			cur.WriteByte(c)
			i++
		}
	}
	if cur.Len() > 0 {
		args = append(args, strings.TrimSpace(cur.String()))
	}
	return args, true
}

// asStringLiteral unquotes a (possibly N-prefixed) SQL string literal into
// its textual value, collapsing ” escape pairs into single quotes. Returns
// ok=false when the input is not a well-formed literal.
func asStringLiteral(s string) (string, bool) {
	if len(s) >= 2 && s[0] == 'N' {
		s = s[1:]
	}
	if len(s) < 2 || s[0] != '\'' || s[len(s)-1] != '\'' {
		return "", false
	}
	inner := s[1 : len(s)-1]
	return strings.ReplaceAll(inner, "''", "'"), true
}

// paramNameRe captures `@P0`, `@P12`, etc. from a declaration string.
var paramNameRe = regexp.MustCompile(`@P\d+`)

// substituteParams returns `template` with @P0/@P1/... replaced by the
// corresponding entries in `values`. Extra values beyond the declared
// parameter count are ignored; missing values leave the placeholder in
// place so the output still parses as SQL.
func substituteParams(template, paramDecl string, values []string) string {
	names := paramNameRe.FindAllString(paramDecl, -1)
	// If the decl didn't list names, infer them from the template itself
	// so sp_executesql without an explicit decl still substitutes.
	if len(names) == 0 {
		names = paramNameRe.FindAllString(template, -1)
		names = dedupePreserveOrder(names)
	}

	out := template
	for i, name := range names {
		if i >= len(values) {
			break
		}
		literal := formatArgForDisplay(values[i])
		// Replace only whole-word occurrences: `@P0` should not match
		// inside `@P01`. Use a bounded regexp per name.
		re := regexp.MustCompile(regexp.QuoteMeta(name) + `\b`)
		out = re.ReplaceAllLiteralString(out, literal)
	}
	return strings.TrimSpace(out)
}

func dedupePreserveOrder(in []string) []string {
	seen := make(map[string]struct{}, len(in))
	out := in[:0]
	for _, s := range in {
		if _, ok := seen[s]; ok {
			continue
		}
		seen[s] = struct{}{}
		out = append(out, s)
	}
	return out
}

// formatArgForDisplay normalizes a raw argument token into a form suitable
// for inlining into a SQL statement. String literals keep their quotes,
// numbers render bare, NULL renders as-is, and anything else is returned
// verbatim.
func formatArgForDisplay(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "NULL"
	}
	if strings.EqualFold(raw, "NULL") {
		return "NULL"
	}
	if val, ok := asStringLiteral(raw); ok {
		// Re-quote with single quotes so the inlined SQL is syntactically
		// valid; drop the N-prefix since the literal is already a string.
		return "'" + strings.ReplaceAll(val, "'", "''") + "'"
	}
	if _, err := strconv.ParseFloat(raw, 64); err == nil {
		return raw
	}
	return raw
}
