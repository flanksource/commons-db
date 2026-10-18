package git

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"regexp"
	"strings"
)

// Ported from github.com/flanksource/gavel/git/refname.go: gavel depends on
// commons-db, so commons-db cannot import it.

var (
	// revisionPattern is one argv token with no whitespace or control
	// character that cannot be read as an option.
	revisionPattern = regexp.MustCompile(`^[^-\s\x00-\x1f\x7f][^\s\x00-\x1f\x7f]*$`)
	// refNamePattern is the character set `git check-ref-format --branch`
	// allows, with a leading "-" excluded.
	refNamePattern = regexp.MustCompile(`^[^-\x00-\x20\x7f~^:?*\[\\][^\x00-\x20\x7f~^:?*\[\\]*$`)
	refDirUnsafe   = regexp.MustCompile(`[^A-Za-z0-9._-]+`)
)

// ValidateRevision rejects a revision that git could read as something other
// than a revision before it is ever placed on a git command line. A leading "-"
// would be parsed as an option (`--upload-pack=…` runs a program), and
// whitespace or a control character means the value was not one revision to
// begin with. Revision syntax itself is left to git. Callers still put
// `--end-of-options` ahead of the value.
func ValidateRevision(rev string) error {
	switch {
	case rev == "":
		return fmt.Errorf("invalid revision: is empty")
	case strings.HasPrefix(rev, "-"):
		return fmt.Errorf("invalid revision %q: must not begin with %q, git parses that as an option", rev, "-")
	case !revisionPattern.MatchString(rev):
		return fmt.Errorf("invalid revision %q: contains whitespace or control character", rev)
	}
	return nil
}

// ValidateBranchName applies the rules of `git check-ref-format --branch`
// without spawning git, so a branch name from a request is refused before it
// reaches `git branch`, `git push` or a refspec.
func ValidateBranchName(name string) error {
	return validateRefName("branch", name)
}

// ValidateTagName applies the same ref-format rules to a tag name.
func ValidateTagName(name string) error {
	return validateRefName("tag", name)
}

func validateRefName(kind, name string) error {
	switch {
	case name == "":
		return invalidRefName(kind, name, "is empty")
	case strings.HasPrefix(name, "-"):
		return invalidRefName(kind, name, `must not begin with "-", git parses that as an option`)
	case name == "HEAD", name == "@":
		return invalidRefName(kind, name, fmt.Sprintf("%q is reserved", name))
	case !refNamePattern.MatchString(name):
		return invalidRefName(kind, name, `must not contain a space, a control character, or any of ~^:?*[\`)
	case strings.Contains(name, ".."):
		return invalidRefName(kind, name, `must not contain ".."`)
	case strings.Contains(name, "@{"):
		return invalidRefName(kind, name, `must not contain "@{"`)
	case strings.HasPrefix(name, "/"), strings.HasSuffix(name, "/"):
		return invalidRefName(kind, name, `must not begin or end with "/"`)
	case strings.HasSuffix(name, "."):
		return invalidRefName(kind, name, `must not end with "."`)
	}
	for _, component := range strings.Split(name, "/") {
		switch {
		case component == "":
			return invalidRefName(kind, name, "empty path component")
		case strings.HasPrefix(component, "."):
			return invalidRefName(kind, name, fmt.Sprintf("component %q must not begin with %q", component, "."))
		case strings.HasSuffix(component, ".lock"):
			return invalidRefName(kind, name, fmt.Sprintf("component %q must not end with %q", component, ".lock"))
		}
	}
	return nil
}

// invalidRefName is a plain function rather than a closure over name: CodeQL
// infers the validators as sanitizing guards only while name stays an
// uncaptured parameter.
func invalidRefName(kind, name, reason string) error {
	return fmt.Errorf("invalid %s name %q: %s", kind, name, reason)
}

// refDirName maps a ref to a single, filesystem-safe directory name. The hash
// suffix keeps refs that sanitize alike (feature/a, feature-a) apart.
func refDirName(ref string) string {
	sum := sha256.Sum256([]byte(ref))
	readable := strings.Trim(refDirUnsafe.ReplaceAllString(ref, "-"), "-.")
	if len(readable) > 60 {
		readable = readable[:60]
	}
	return readable + "-" + hex.EncodeToString(sum[:])[:8]
}
