package xetrace

import "strings"

// RPCCall retains a prepared statement's template independently of its values.
type RPCCall struct {
	Template  string
	ParamDecl string
	Values    []string
}

// ParseRPC decodes the parameter-bearing RPC forms captured by SQL Server.
func ParseRPC(raw string) (*RPCCall, bool) {
	text := strings.TrimSpace(raw)
	body, prepared := stripPrepexecScaffold(text)
	if !prepared {
		loc := executeSQLRe.FindStringIndex(text)
		if loc == nil {
			return nil, false
		}
		body = text[loc[1]:]
	}
	args, ok := splitTopLevelArgs(body)
	if !ok || len(args) == 0 || (prepared && len(args) < 2) {
		return nil, false
	}
	call := &RPCCall{}
	if prepared {
		if !strings.EqualFold(strings.TrimSpace(args[0]), "NULL") {
			call.ParamDecl, ok = asStringLiteral(args[0])
			if !ok {
				return nil, false
			}
		}
		args = args[1:]
	}
	call.Template, ok = asStringLiteral(args[0])
	if !ok {
		return nil, false
	}
	args = args[1:]
	if !prepared && len(args) > 0 {
		if decl, found := asStringLiteral(args[0]); found {
			call.ParamDecl = decl
			args = args[1:]
		}
	}
	call.Values = args
	return call, true
}

func collapseWhitespace(s string) string {
	return strings.Join(strings.Fields(s), " ")
}
