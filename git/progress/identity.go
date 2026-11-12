package progress

import (
	"github.com/flanksource/commons-db/git"
)

func repositoryName(raw string) string {
	parsed, err := git.ParseRepoURL(raw, "")
	if err != nil {
		panic(err)
	}
	return parsed.DisplayName()
}

func taskName(repository, operation, ref string) string {
	name := "[" + repositoryName(repository) + "] " + operation
	if resolved, err := (git.RemoteRefs{}).Resolve(ref); err == nil && resolved.Kind == git.RefSHA {
		ref = "@" + ref[:12]
	}
	if ref != "" {
		name += " " + ref
	}
	return name
}
