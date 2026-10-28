package commands

import (
	"regexp"
	"strings"

	"github.com/flanksource/clicky"
	"github.com/flanksource/commons-db/projections"
	"github.com/spf13/cobra"
)

func documentProjections(root *cobra.Command) {
	root.Long = "Project local exec or HTTP source rows into YAML using CEL and JSONPath.\n\nSyntax:\n\n  query projections <verify|run|apply> [path...] [-f PATH ...] [--name NAME] [--context NAME] [flags]\n\nUse verify to validate offline, run to inspect sources, apply --dry-run to preview mappings, and apply to write targets. See query projections syntax --help for full manifests, field descriptions, connections and Faro recipes."
	root.Example = "  query projections verify projections.yaml\n  query projections run projections.yaml --json\n  query projections apply projections.yaml --dry-run\n  query projections apply projections.yaml"
	for _, child := range root.Commands() {
		switch child.Name() {
		case "verify":
			child.Short = "Validate manifests and targets offline"
			child.Long = "Validate `apiVersion`, `kind`, unique `metadata.name`, source options, CEL expressions, writable JSONPaths, target files and optional JSON Schemas. Executes no commands and resolves no credentials."
		case "run":
			child.Short = "Execute sources and inspect filtered JSON rows"
			child.Long = "Execute `exec` or `http` sources and return source rows without applying mappings. Compiles all CEL expressions first. `source.where` filters rows. `--context` sets `context.name`; the date and observation time are generated per run."
		case "apply":
			child.Short = "Apply source mappings to YAML targets"
			child.Long = "Execute sources, match rows with `spec.match`, and apply `spec.set` mappings to existing YAML files. `--dry-run` evaluates changes without writing targets. Projections execute sequentially; individual failures are reported while later projections continue. Successful writes remain on disk."
		case "syntax":
			child.Long = projections.Syntax()
		}
		if child.Name() != "syntax" {
			child.Use += " [path...]"
			child.Long += "\n\nSupply positional paths and/or repeatable --file/-f. Use --name for one exact metadata.name. Directories recurse through YAML files in lexical order, skipping foreign documents. Paths resolve relative to each manifest. No query profile or database is needed.\n\nFull syntax: query projections syntax --help"
			child.Example = "  query projections " + child.Name() + " projections.yaml\n  query projections " + child.Name() + " projections/ -f extra.yaml --name inventory --json"
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
