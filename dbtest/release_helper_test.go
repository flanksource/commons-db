package dbtest

import (
	"context"
	"fmt"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"
)

const releaseHelperAdminURLEnv = "DBTEST_RELEASE_HELPER_ADMIN_URL"

// TestReleaseInBackgroundHelper is the child process for the "completes a
// background drop after the releasing process exits" spec: it leases a
// database, prints its name, releases it in the background and exits at once.
func TestReleaseInBackgroundHelper(t *testing.T) {
	adminURL := os.Getenv(releaseHelperAdminURLEnv)
	if adminURL == "" {
		return
	}
	dsn, lease, err := createLeasedDatabase(context.Background(), adminURL, "exit release", uniqueSuffix(), time.Now())
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}
	parsed, err := url.Parse(dsn)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}
	fmt.Println(strings.TrimPrefix(parsed.Path, "/"))
	lease.releaseInBackground()
	os.Exit(0)
}
