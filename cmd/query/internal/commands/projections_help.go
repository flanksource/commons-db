package commands

import (
	"regexp"
	"strings"

	"github.com/flanksource/clicky"
	"github.com/flanksource/commons-db/projections"
	"github.com/spf13/cobra"
)

func documentProjections(root *cobra.Command) {
	root.Long = projectionOverviewHelp + projectionInputHelp
	root.Example = "  query projections verify projections.yaml\n  query projections run projections.yaml --json\n  query projections apply projections.yaml --dry-run\n  query projections apply projections.yaml"
	for _, child := range root.Commands() {
		switch child.Name() {
		case "verify":
			child.Short = "Validate manifests and targets offline"
			child.Long = projectionVerifyHelp
		case "run":
			child.Short = "Execute sources and inspect filtered JSON rows"
			child.Long = projectionRunHelp
		case "apply":
			child.Short = "Apply source mappings to YAML targets"
			child.Long = projectionApplyHelp
			child.Example = "  query projections apply projections.yaml --dry-run --yaml\n  query projections apply projections.yaml --name inventory --context staging --dry-run\n  query projections apply projections/ --json --no-progress"
		case "syntax":
			child.Long = projections.Syntax()
		}
		if child.Name() != "syntax" {
			child.Use += " [path...]"
			child.Long += projectionInputHelp
			if child.Example == "" {
				child.Example = "  query projections " + child.Name() + " projections.yaml\n  query projections " + child.Name() + " projections/ -f extra.yaml --name inventory --json"
			}
		}
	}
	if root.PersistentFlags().Lookup("no-color") == nil {
		root.PersistentFlags().Bool("no-color", false, "Disable colored help and output")
	}
	cobra.AddTemplateFunc("queryProjectionHelp", renderProjectionHelp)
	root.SetHelpTemplate("{{queryProjectionHelp . .Long}}\n\n{{.UsageString}}")
	usage := strings.ReplaceAll(root.UsageTemplate(), "{{.Example}}", "{{queryProjectionHelp . .Example}}")
	usage = strings.Replace(usage, "{{if .Runnable}}", "{{if or .Runnable .IsAdditionalHelpTopicCommand}}", 1)
	root.SetUsageTemplate(usage)
}

const projectionOverviewHelp = `Project JSON from local commands or HTTP APIs into existing YAML files using CEL for matching and mapping and JSONPath for field selection. Comments, flow collections and indentation are preserved.

Syntax:

  query projections <verify|run|apply> [path...] [-f PATH ...] [--name NAME] [--context NAME] [flags]

Workflow:

Use verify to check manifests and existing targets offline. Use run to inspect source rows. Use apply --dry-run to evaluate mappings and preview the change summary, then apply to write target files.

Manifest syntax:

Each YAML document requires apiVersion: projections.flanksource.com/v1alpha1, kind: Projection, a unique metadata.name, and a spec. Separate multiple documents with ---. Choose exactly one spec.source.query: exec or http. A source-only exec projection in once mode can omit target, match, and set.

` + "```yaml\n" + `apiVersion: projections.flanksource.com/v1alpha1
kind: Projection
metadata:
  name: inventory
spec:
  source:
    query:
      exec:
        script: printf '%s' '{"id":"api","name":"API service"}'
  target:
    path: target.yaml
    select: $.entries[*]
  match:
    - source.id == target.id
  set:
    $.id:
      value: source.id
    $.name:
      value: source.name
` + "```" + `

Save the manifest as projections.yaml beside an existing target.yaml containing entries: []. Paths resolve relative to the manifest. See apply --help for matching, list strategies, creation and aggregation rules.`

const projectionInputHelp = `

Selecting manifests:

Supply at least one file or directory as a positional path or repeatable --file/-f flag; both forms can be combined. Directories are searched recursively for .yaml and .yml files in lexical order, skipping non-Projection documents. Explicit files must contain Projection documents. Repeated paths are loaded once; names must be unique across all loaded documents. --name selects one exact metadata.name after loading. Target and schema paths resolve relative to each manifest. No query profile or database is needed.

Output:

Use --json, --yaml, --csv, or --markdown to select the result format. Use --no-progress for scripts. --no-color or NO_COLOR=1 disables colored help and output.

Full syntax reference:

  query projections syntax --help

The syntax reference includes complete exec and HTTP manifests, target YAML, connections, environment settings and Faro recipes.`

const projectionRunHelp = `Execute each selected Projection's source query and return its rows without applying mappings or writing target files.

Syntax:

  query projections run [path...] [-f PATH ...] [--name NAME] [--context NAME] [--yaml|--json]

Source queries:

exec runs a local script in the manifest's directory. mode defaults to once: stdout must contain one JSON object or an array of objects after optional select. With mode: perTarget, the script runs once per eligible target entry; each JSON output becomes source.body alongside the target's scalar fields. Scripts use Go templates with .context and, in perTarget mode, .target. Use {{shellQuote .target.id}} for shell arguments. timeout defaults to one minute per invocation; failed commands abort the projection.

http requires existing targets and sends one request per eligible entry. Its URL is a Go template over the entry's fields, such as {{.id}}. method defaults to GET. source.body holds the response or the single value selected by select; source.url and the target's scalar fields are included. HTTP 202 responses are retried according to accepted.attempts and accepted.wait. Other request or response failures omit the row and produce warnings.

Filtering and validation:

spec.source.where is a CEL predicate over source and context, evaluated per source row. False rows are excluded and counted as filtered. spec.target.filter limits eligible targets before per-target exec or HTTP requests.

All selected CEL expressions are compiled before querying, including target filters, matches and set mappings. Invalid mappings therefore fail run even though they are not applied. Use verify to check existing targets and their schemas.

Results and connections:

Each result contains projection, context, items, filtered, and any warnings. --context supplies context.name; it does not select a query profile or Faro context. context.observed_at is an RFC3339 timestamp and context.date is YYYY-MM-DD, generated per run.

Named connections resolve only from the local Flanksource connection store. Exec sources support typed connections, env and passthroughEnv; HTTP sources support shared HTTPConnection authentication. run executes scripts and sends HTTP requests using the configured method.`

const projectionApplyHelp = `Execute sources, match source rows to target entries, and apply CEL mappings to local YAML files. Use --dry-run to evaluate mappings and report changes without writing targets.

Syntax:

  query projections apply [path...] [-f PATH ...] [--name NAME] [--dry-run] [--context NAME] [flags]

Matching and mapping syntax:

spec.target.path names an existing YAML file. spec.target.select is a JSONPath ending in [*], such as $.entries[*], selecting mapping entries. spec.target.filter optionally limits eligible targets with CEL. Each spec.match expression is a CEL predicate over source, target, and context; a target matches when any expression is true. A source matching multiple targets is an error.

Each spec.set key is a JSONPath relative to the selected entry, such as $.name. Its value is a CEL expression, such as source.name; when is an optional CEL guard. Quote CEL string literals inside YAML, for example value: '"managed"'. Expressions can use source, target, context.name, context.observed_at, and context.date. Guards see the original target for that source row and can protect optional fields, for example when: '"email" in source'.

List strategies:

replace is the default: replace the field with the evaluated value. mergeUnique appends incoming list values that are not already present. replaceMatching removes existing list items for which its match predicate is true, then appends incoming values; item refers to an existing list element, for example item.context == context.name.

Creation and aggregation:

Unmatched source rows create entries in the selected list by default, using spec.set to populate them. Set spec.target.create: false to report them as missing instead. With spec.target.aggregate: true, multiple sources may share a target; use mergeUnique or replaceMatching for lists, or guard scalar writes with when. Conflicting scalar writes fail without writing that projection. Eligible entries without a source are reported as stale when target.filter is set; they are retained.

Validation and execution:

Target and optional JSON Schema paths are relative to the manifest. Targets are schema-checked before and after mapping when a schema is declared. All selected CEL programs are compiled before any query. --context supplies context.name, without selecting a query profile or Faro context. Projections execute sequentially, including when they share a target file.

Source-only projections are skipped; selecting only source-only projections is an error. A data or mapping failure in one projection does not prevent later projections from running. Successful writes remain on disk, and the command exits with an error if any projection failed.

Dry runs and results:

--dry-run still executes sources and validates the mapped document, but leaves target files unchanged. It reports a summary, not a rendered target or patch. Results include status (applied, warned, skipped, or failed), source and match counts, changed fields, created and missing entries, stale entries, warnings, and errors.`

const projectionVerifyHelp = `Validate selected Projection manifests and their existing target files offline, without executing scripts, resolving credentials, making HTTP requests or writing target files.

Syntax:

  query projections verify [path...] [-f PATH ...] [--name NAME] [--json|--yaml]

Manifest checks:

Check YAML fields, apiVersion, kind, unique metadata.name values, exactly one source query, and exec or HTTP options. Compile CEL expressions in source.where, target.filter, match, set.value, set.when and set.match. Validate mapping JSONPaths and list strategies.

Target checks:

If spec.target is present, read its existing file, check that target.select returns mapping entries, and validate writable mapping paths. target.select must end in [*], for example $.entries[*]. Validate the complete target document against spec.target.schema when provided. Target and schema paths resolve relative to the manifest, regardless of the current working directory.

For HTTP sources, validate the URL template against existing target entries without making requests. Exec script templates are parsed without running commands. Source-only exec projections in once mode are valid and require no target file.

Results and limits:

Return the manifest files actually verified and their projection names. Stop at the first error with a nonzero exit status. Verification checks expression syntax and existing target structure; it cannot prove that expressions will evaluate successfully against future source rows. Use run to inspect those rows, then apply --dry-run to evaluate mappings.`

var projectionInlineCode = regexp.MustCompile("`([^`]+)`")

func renderProjectionHelp(command *cobra.Command, value string) (string, error) {
	noColor, err := command.Flags().GetBool("no-color")
	if err != nil {
		return "", err
	}
	options := clicky.FormatOptions{NoColor: noColor}
	options.ResolveNoColor()
	if options.NoColor {
		return value, nil
	}
	parts := strings.Split(value, "```")
	for i, part := range parts {
		if i%2 == 1 {
			language, code, _ := strings.Cut(part, "\n")
			parts[i] = clicky.CodeBlock(strings.TrimSpace(language), code).ANSI()
			continue
		}
		var rendered strings.Builder
		for _, line := range strings.SplitAfter(part, "\n") {
			if strings.HasPrefix(line, "#") || strings.HasSuffix(strings.TrimSpace(line), ":") {
				rendered.WriteString(clicky.Text(line, "text-cyan-500 font-bold").ANSI())
			} else {
				rendered.WriteString(projectionInlineCode.ReplaceAllStringFunc(line, func(field string) string { return clicky.CodeBlock("yaml", strings.Trim(field, "`")).ANSI() }))
			}
		}
		parts[i] = rendered.String()
	}
	return strings.Join(parts, ""), nil
}
