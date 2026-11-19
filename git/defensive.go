// Limits a defensive client's git commands to remotes on public addresses, as commons
// http.Defensive limits its provider API calls.
package git

import (
	"cmp"
	"context"
	"fmt"
	"net"
	"strings"
)

// carrierGradeNAT is the shared address space (RFC 6598), which is not public.
var carrierGradeNAT = &net.IPNet{IP: net.IPv4(100, 64, 0, 0), Mask: net.CIDRMask(10, 32)}

// defensiveRemote refuses a remote a defensive client may not reach: one not over https or ssh,
// or whose host is, or resolves to, an address that is not public. It applies the rule commons
// http.Defensive applies to the addresses it dials. git resolves the host again when it
// connects, so a DNS answer that changes in between is not caught here.
func defensiveRemote(ctx context.Context, remote RepoURL) error {
	if remote.Scheme != "https" && remote.Scheme != "ssh" {
		return fmt.Errorf("a defensive client reaches git remotes over https or ssh only, not %s", cmp.Or(remote.Scheme, "a local path"))
	}
	host := remote.Host
	if at := strings.LastIndex(host, "@"); at >= 0 {
		host = host[at+1:]
	}
	if split, _, err := net.SplitHostPort(host); err == nil {
		host = split
	}
	host = strings.Trim(host, "[]")
	if ip := net.ParseIP(host); ip != nil {
		return publicAddress(ip)
	}
	addresses, err := net.DefaultResolver.LookupIPAddr(ctx, host)
	if err != nil {
		return fmt.Errorf("resolve git remote host %q: %w", host, err)
	}
	if len(addresses) == 0 {
		return fmt.Errorf("resolve git remote host %q: no addresses returned", host)
	}
	for _, address := range addresses {
		if err := publicAddress(address.IP); err != nil {
			return fmt.Errorf("git remote host %q resolves to %s: %w", host, address.IP, err)
		}
	}
	return nil
}

// publicAddress refuses an address that is not public.
func publicAddress(ip net.IP) error {
	switch {
	case ip.IsUnspecified() || ip.IsMulticast() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast():
		return fmt.Errorf("defensive git client refuses reserved address %s", ip)
	case ip.IsLoopback():
		return fmt.Errorf("defensive git client refuses localhost address %s", ip)
	case ip.IsPrivate() || carrierGradeNAT.Contains(ip):
		return fmt.Errorf("defensive git client refuses private address %s", ip)
	case !ip.IsGlobalUnicast():
		return fmt.Errorf("defensive git client refuses non-public address %s", ip)
	}
	return nil
}

// lsRemoteDefensive lists a defensive repository's remote with the git binary, whose network
// access commandEnv limits, instead of go-git's process-wide transports.
func lsRemoteDefensive(ctx context.Context, repo *Repository) (RemoteRefs, error) {
	env, cleanup, err := commandEnv(ctx, repo)
	if err != nil {
		return RemoteRefs{}, fmt.Errorf("ls-remote %w", err)
	}
	defer cleanup()
	out, err := run(ctx, command{env: env, args: []string{"ls-remote", "--symref", "--end-of-options", repo.URL.Clone}})
	if err != nil {
		return RemoteRefs{}, fmt.Errorf("ls-remote %s: %w", repo.URL.Redacted(), err)
	}
	list, err := parseLsRemote(out)
	if err != nil {
		return RemoteRefs{}, fmt.Errorf("ls-remote %s: %w", repo.URL.Redacted(), err)
	}
	return refsFromList(list), nil
}
