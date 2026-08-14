package k8s

import (
	"bufio"
	"fmt"
	"maps"
	"strconv"
	"strings"
	"time"

	"github.com/samber/lo"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"

	"github.com/flanksource/commons-db/context"
	"github.com/flanksource/commons-db/logs"
	"github.com/flanksource/commons-db/types"
)

// Request represents available parameters for Kubernetes log queries.
//
// +kubebuilder:object:generate=true
type Request struct {
	logs.LogsRequestBase `json:",inline" yaml:",inline" template:"true"`

	// Logs will include pods that match any of these selectors.
	//
	// This applies when retrieving logs at a higher resource level,
	// such as fetching logs for a deployment spanning multiple pods.
	Pods types.ResourceSelectors `json:"pods,omitempty"`

	// Containers filters logs from only these containers.
	Containers types.MatchExpressions `json:"containers,omitempty"`
}

// Fetch reads logs from the already-resolved pods. Target discovery belongs to
// the query provider so one selector and one resource cap govern every kind.
func Fetch(ctx context.Context, client kubernetes.Interface, pods []corev1.Pod, request Request) ([]logs.LogResult, error) {
	var logGroups []logs.LogResult
	for _, pod := range pods {
		if logs, err := fetchPodLogs(ctx, client, pod, request); err != nil {
			return nil, err
		} else if logs != nil {
			logGroups = append(logGroups, logs...)
		}
	}

	return logGroups, nil
}

// SelectPods narrows already-resolved pods to those a selection matches. An
// empty selection matches everything, so a caller with nothing to narrow by
// passes it through unchanged.
//
// It exists as its own gate rather than as more entries on Request.Pods because
// ResourceSelectors are a union: one list holding a profile's declared pods and
// an operator's picked pod would read the two as alternatives and return both.
func SelectPods(pods []corev1.Pod, selectors types.ResourceSelectors) []corev1.Pod {
	if len(selectors) == 0 {
		return pods
	}
	matched := make([]corev1.Pod, 0, len(pods))
	for _, pod := range pods {
		if selectors.Matches(podSelectable(pod)) {
			matched = append(matched, pod)
		}
	}
	return matched
}

// podSelectable exposes a pod to the resource-selector machinery.
//
// id and name are always set: ResourceSelectableMap reads them with a bare type
// assertion, so a missing key panics rather than failing to match.
func podSelectable(pod corev1.Pod) types.ResourceSelectableMap {
	return types.ResourceSelectableMap{
		"id":        string(pod.UID),
		"name":      pod.Name,
		"namespace": pod.Namespace,
		"labels":    map[string]string(pod.Labels),
	}
}

func fetchPodLogs(ctx context.Context, client kubernetes.Interface, pod corev1.Pod, request Request) ([]logs.LogResult, error) {
	if len(request.Pods) > 0 && !request.Pods.Matches(podSelectable(pod)) {
		return nil, nil
	}

	var logGroups []logs.LogResult
	if len(request.Containers) == 0 {
		if logs, err := fetchContainerLogs(ctx, client, pod, "", request); err != nil {
			return nil, err
		} else if logs != nil {
			logGroups = append(logGroups, *logs)
		}
	} else {
		for _, container := range pod.Spec.Containers {
			if request.Containers.Match(container.Name) {
				if logs, err := fetchContainerLogs(ctx, client, pod, container.Name, request); err != nil {
					return nil, err
				} else if logs != nil {
					logGroups = append(logGroups, *logs)
				}
			}
		}
	}

	return logGroups, nil
}

func fetchContainerLogs(ctx context.Context, client kubernetes.Interface, pod corev1.Pod, containerName string, request Request) (*logs.LogResult, error) {
	opt := &corev1.PodLogOptions{
		Container:  containerName,
		Timestamps: true,
	}

	// A start nobody could parse used to mean "from the beginning of the
	// container's history", which is the most expensive read available and the
	// opposite of what a narrowing bound asked for.
	if request.Start != "" {
		start, err := request.GetStart()
		if err != nil {
			return nil, fmt.Errorf("start %q is not a time or date math: %w", request.Start, err)
		}
		opt.SinceTime = &metav1.Time{Time: start}
	}

	// The kubelet serves no upper bound, so an end is enforced while scanning.
	var end time.Time
	if request.End != "" {
		parsed, err := request.GetEnd()
		if err != nil {
			return nil, fmt.Errorf("end %q is not a time or date math: %w", request.End, err)
		}
		end = parsed
	}

	if request.Limit != "" {
		limit, err := strconv.ParseInt(request.Limit, 10, 32)
		if err != nil {
			return nil, err
		}
		opt.TailLines = lo.ToPtr(limit)
	}

	req := client.CoreV1().Pods(pod.Namespace).GetLogs(pod.Name, opt)
	podLogs, err := req.Stream(ctx)
	if err != nil {
		return nil, err
	}
	defer podLogs.Close()

	// Search returns one LogResult per container, so without this the caller
	// cannot tell which pod or container a line came from — Host carries the
	// pod name, but namespace and container would be lost entirely.
	//
	// An unspecified container is not an unknown one: the kubelet serves the
	// pod's first, so report that rather than leaving every line of a
	// single-container pod unattributed.
	reportedContainer := containerName
	if reportedContainer == "" && len(pod.Spec.Containers) > 0 {
		reportedContainer = pod.Spec.Containers[0].Name
	}

	output := logs.LogResult{
		Metadata: map[string]any{
			"pod":       pod.Name,
			"namespace": pod.Namespace,
			"container": reportedContainer,
		},
	}
	scanner := bufio.NewScanner(podLogs)
	for scanner.Scan() {
		parts := strings.SplitN(scanner.Text(), " ", 2)
		if len(parts) < 2 {
			continue
		}

		t, err := time.Parse(time.RFC3339, parts[0])
		if err != nil {
			continue
		}
		if !end.IsZero() && t.After(end) {
			continue
		}

		line := &logs.LogLine{
			// The id is what makes the result's order total: a timestamp ties
			// across containers, and a line's position in its own stream is the
			// only thing that never does.
			ID:      fmt.Sprintf("%s/%s/%s#%d", pod.Namespace, pod.Name, reportedContainer, len(output.Logs)),
			Count:   1,
			Message: parts[1],
			// Cloned: pod.Labels belongs to the client-go object and is shared
			// by every line, so handing it out unowned lets one line's edit
			// reach all of them and the API response besides.
			Labels:        maps.Clone(pod.Labels),
			Host:          pod.Name,
			FirstObserved: t,
		}
		line.SetHash()
		output.Logs = append(output.Logs, line)
	}

	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("error reading logs: %w", err)
	}

	return &output, nil
}
