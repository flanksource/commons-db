package git

import (
	"regexp"
	"strings"

	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/ast"
	"github.com/yuin/goldmark/text"
)

var nativeMentionAnchor = regexp.MustCompile(`(?is)<a\b[^>]*\bdata-vss-mention\s*=\s*["'][^"']*\bid:([^"',\s]+)[^"']*["'][^>]*>.*?</a>`)
var activityMention = regexp.MustCompile(`(?i)(^|[^a-z0-9_@./-])@(?:<([^>\s]+)>|([a-z0-9][a-z0-9_.-]*(?:/[a-z0-9][a-z0-9_.-]*)*))`)

// ActivityMentions returns canonical mention identities in prose order, excluding code and email addresses.
func ActivityMentions(body string) []string {
	prose := nativeMentionAnchor.ReplaceAllString(mentionText(body), " @<$1> ")
	mentions := []string{}
	seen := map[string]bool{}
	for _, match := range activityMention.FindAllStringSubmatch(prose, -1) {
		identity := match[2]
		if identity == "" {
			identity = strings.TrimRight(match[3], ".")
		}
		identity = strings.ToLower(identity)
		if !seen[identity] {
			mentions = append(mentions, identity)
			seen[identity] = true
		}
	}
	return mentions
}

func mentionText(body string) string {
	source := []byte(body)
	document := goldmark.DefaultParser().Parse(text.NewReader(source))
	var prose strings.Builder
	var visit func(ast.Node)
	visit = func(node ast.Node) {
		switch node := node.(type) {
		case *ast.CodeSpan, *ast.CodeBlock, *ast.FencedCodeBlock, *ast.AutoLink:
			prose.WriteByte(' ')
			return
		case *ast.Text:
			prose.Write(node.Segment.Value(source))
		case *ast.RawHTML:
			for i := 0; i < node.Segments.Len(); i++ {
				segment := node.Segments.At(i)
				prose.Write(segment.Value(source))
			}
		case *ast.HTMLBlock:
			prose.Write(node.Lines().Value(source))
		}
		for child := node.FirstChild(); child != nil; child = child.NextSibling() {
			visit(child)
		}
		if node.Type() == ast.TypeBlock {
			prose.WriteByte('\n')
		}
	}
	visit(document)
	return prose.String()
}
