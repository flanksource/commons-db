package kubernetes

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	clientcmdapi "k8s.io/client-go/tools/clientcmd/api"
)

type Options struct {
	Config      []byte `json:"-" yaml:"-"`
	Kubeconfig  string `json:"kubeconfig,omitempty" yaml:"kubeconfig,omitempty"`
	Kubecontext string `json:"kubecontext,omitempty" yaml:"kubecontext,omitempty"`
}

func (o Options) RESTConfig() (*rest.Config, error) {
	if !o.Explicit() {
		if cfg, err := rest.InClusterConfig(); err == nil {
			return cfg, nil
		}
	}
	client, err := o.clientConfig()
	if err != nil {
		return nil, err
	}
	cfg, err := client.ClientConfig()
	if err != nil {
		return nil, fmt.Errorf("kubernetes selection (kubeconfig %q, kubecontext %q): %w", o.Kubeconfig, o.Kubecontext, err)
	}
	return cfg, nil
}

func (o Options) Explicit() bool {
	return len(o.Config) > 0 || strings.TrimSpace(o.Kubeconfig) != "" || strings.TrimSpace(o.Kubecontext) != ""
}

func (o Options) Namespace() (string, error) {
	client, err := o.clientConfig()
	if err != nil {
		return "", err
	}
	ns, _, err := client.Namespace()
	if err != nil {
		return "", fmt.Errorf("resolve kubernetes namespace: %w", err)
	}
	return ns, nil
}

type Contexts struct {
	CurrentContext string   `json:"currentContext"`
	Contexts       []string `json:"contexts"`
}

func (o Options) Contexts() (Contexts, error) {
	raw, err := o.load()
	if err != nil {
		return Contexts{}, err
	}
	names := make([]string, 0, len(raw.Contexts))
	for name := range raw.Contexts {
		names = append(names, name)
	}
	slices.Sort(names)
	return Contexts{CurrentContext: raw.CurrentContext, Contexts: names}, nil
}

func (o Options) ContextName() (string, error) {
	if !o.Explicit() {
		if _, err := rest.InClusterConfig(); err == nil {
			return "", nil
		}
	}
	client, err := o.clientConfig()
	if err != nil {
		return "", err
	}
	raw, err := client.RawConfig()
	if err != nil {
		return "", err
	}
	if name := strings.TrimSpace(o.Kubecontext); name != "" {
		return name, nil
	}
	return raw.CurrentContext, nil
}

func (o Options) clientConfig() (clientcmd.ClientConfig, error) {
	raw, err := o.load()
	if err != nil {
		return nil, err
	}
	name := strings.TrimSpace(o.Kubecontext)
	if name != "" && raw.Contexts[name] == nil {
		available, err := o.Contexts()
		if err != nil {
			return nil, err
		}
		return nil, fmt.Errorf("kube context %q not found in kubeconfig %q (available: %s)", name, o.Kubeconfig, strings.Join(available.Contexts, ", "))
	}
	return clientcmd.NewDefaultClientConfig(*raw, &clientcmd.ConfigOverrides{CurrentContext: name}), nil
}

func (o Options) load() (*clientcmdapi.Config, error) {
	if len(o.Config) > 0 {
		if strings.TrimSpace(o.Kubeconfig) != "" {
			return nil, fmt.Errorf("specify kubeconfig content or a file, not both")
		}
		return clientcmd.Load(o.Config)
	}
	rules := clientcmd.NewDefaultClientConfigLoadingRules()
	file := strings.TrimSpace(o.Kubeconfig)
	if strings.HasPrefix(file, "~/") {
		home, err := os.UserHomeDir()
		if err != nil {
			return nil, fmt.Errorf("resolve kubeconfig home directory: %w", err)
		}
		file = filepath.Join(home, file[2:])
	}
	rules.ExplicitPath = file
	raw, err := rules.Load()
	if err != nil {
		return nil, fmt.Errorf("load kubeconfig %v: %w", rules.GetLoadingPrecedence(), err)
	}
	return raw, nil
}
