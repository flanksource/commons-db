package kubernetes

import (
	"context"
	"net/http"
	"os"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/flanksource/commons/files"
	"github.com/flanksource/commons/logger"
	"github.com/henvic/httpretty"
	"gopkg.in/yaml.v2"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/kubernetes/fake"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
)

var Nil = fake.NewSimpleClientset()

func NewClient(logger logger.Logger, kubeconfigPaths ...string) (kubernetes.Interface, *rest.Config, error) {
	if len(kubeconfigPaths) == 0 {
		kubeconfigPaths = []string{os.Getenv("KUBECONFIG"), os.ExpandEnv("$HOME/.kube/config")}
	}

	for _, path := range kubeconfigPaths {
		if files.Exists(path) {
			if configBytes, err := os.ReadFile(path); err != nil {
				return nil, nil, err
			} else {
				logger.Infof("Using kubeconfig %s", path)
				return NewClientWithConfig(logger, configBytes)
			}
		}
	}

	if config, err := rest.InClusterConfig(); err == nil {
		client, err := kubernetes.NewForConfig(trace(logger, config))
		return client, config, err
	}
	return Nil, nil, nil
}

func NewClientWithConfig(logger logger.Logger, kubeConfig []byte) (kubernetes.Interface, *rest.Config, error) {
	clientConfig, err := clientcmd.NewClientConfigFromBytes(kubeConfig)
	if err != nil {
		return nil, nil, err
	}

	if config, err := clientConfig.ClientConfig(); err != nil {
		return nil, nil, err
	} else {
		client, err := kubernetes.NewForConfig(trace(logger, config))
		return client, config, err
	}
}

func trace(clogger logger.Logger, config *rest.Config) *rest.Config {
	if clogger.IsLevelEnabled(7) {
		clogger.Infof("tracing kubernetes API calls")
		logger := &httpretty.Logger{
			Time:           true,
			TLS:            clogger.IsLevelEnabled(8),
			RequestHeader:  true,
			RequestBody:    clogger.IsLevelEnabled(9),
			ResponseHeader: true,
			ResponseBody:   clogger.IsLevelEnabled(8),
			Colors:         true, // erase line if you don't like colors
			Formatters:     []httpretty.Formatter{&httpretty.JSONFormatter{}},
		}

		config.WrapTransport = func(rt http.RoundTripper) http.RoundTripper {
			return logger.RoundTripper(rt)
		}
	}
	return config
}

func GetClusterName(config *rest.Config) string {
	clientset, err := kubernetes.NewForConfig(config)
	if err != nil {
		return ""
	}
	kubeadmConfig, err := clientset.CoreV1().ConfigMaps("kube-system").Get(context.TODO(), "kubeadm-config", metav1.GetOptions{})
	if err != nil {
		return ""
	}
	clusterConfiguration := make(map[string]interface{})

	if err := yaml.Unmarshal([]byte(kubeadmConfig.Data["ClusterConfiguration"]), &clusterConfiguration); err != nil {
		return ""
	}
	return clusterConfiguration["clusterName"].(string)
}
