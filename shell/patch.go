package shell

import (
	"fmt"
	"strings"

	"github.com/flanksource/commons-db/context"
)

func validateSetupPatch(setup *Setup) error {
	if strings.TrimSpace(setup.Patch) == "" {
		return nil
	}
	if setup.Checkout == nil || setup.Checkout.Worktree == nil || setup.Checkout.Worktree.Mode != WorktreeNew {
		return fmt.Errorf("setup.patch requires an isolated new Git worktree")
	}
	return nil
}

func applySetupPatch(ctx context.Context, setup *Setup, result *SetupResult) error {
	if strings.TrimSpace(setup.Patch) == "" {
		return nil
	}
	patch := []byte(setup.Patch)
	if err := gitApply(ctx, result.Cwd, patch, "apply", "--check", "--binary"); err != nil {
		return fmt.Errorf("check setup patch: %w", err)
	}
	if err := gitApply(ctx, result.Cwd, patch, "apply", "--binary"); err != nil {
		return fmt.Errorf("apply setup patch: %w", err)
	}
	return nil
}
