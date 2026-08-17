package k8s

import (
	"bufio"
	"fmt"
	"maps"
	"strconv"
	"strings"
	"time"

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

	// After resumes a forward walk immediately past a line already read.
	//
	// The kubelet has no cursor and SinceTime only names whole seconds, so a
	// resume re-reads from the start of that second and this is what discards
	// the part already served. Lines are compared on (timestamp, id), which is
	// the order the provider declares.
	After *Position `json:"-"`
}

// Position identifies one log line within the ascending (timestamp, id) order.
type Position struct {
	Timestamp time.Time
	ID        string
}

// reached reports whether line is at or before this position, and so was
// already served by the page that issued it.
func (p Position) reached(timestamp time.Time, id string) bool {
	if timestamp.Before(p.Timestamp) {
		return true
	}
	if timestamp.After(p.Timestamp) {
		return false
	}
	return id <= p.ID
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

	// Limit caps how many lines are read forward from the window's start, not
	// how many are taken off its end.
	//
	// TailLines, which this used to set, counts backwards from the newest line
	// the container ever wrote. That is the wrong end for a forward walk, and it
	// silently disagreed with an End bound too: with both set, the tail lines
	// are all newer than the bound and every one of them is discarded, so a
	// bounded query with a limit returned nothing at all.
	var limit int
	if request.Limit != "" {
		parsed, err := strconv.Atoi(request.Limit)
		if err != nil {
			return nil, fmt.Errorf("limit %q is not a number: %w", request.Limit, err)
		}
		limit = parsed
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
	stream := fmt.Sprintf("%s/%s/%s", pod.Namespace, pod.Name, reportedContainer)
	// ordinal counts the lines sharing one instant, and resets when the instant
	// does. Numbering within the timestamp rather than within the fetch is what
	// makes an id reproducible: a resume re-reads from the start of the cursor's
	// second, so every line of that instant is seen from position 0 again and
	// gets the same id it had on the page that issued the cursor.
	var previous time.Time
	ordinal := 0
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
		if t.Equal(previous) {
			ordinal++
		} else {
			previous, ordinal = t, 0
		}
		// A container's own stream is ascending, so the first line past the end
		// bound ends this read rather than merely being skipped.
		if !end.IsZero() && t.After(end) {
			break
		}
		id := fmt.Sprintf("%s#%d", stream, ordinal)
		if request.After != nil && request.After.reached(t, id) {
			continue
		}
		// One past the cap is read on purpose: the caller cuts at the limit and
		// reads the extra as "there is more", which is a different fact from a
		// page that happened to come up short.
		if limit > 0 && len(output.Logs) > limit {
			break
		}

		line := &logs.LogLine{
			// The id is what makes the result's order total: a timestamp ties
			// across containers, and a line's position within its own instant is
			// the only thing that never does.
			ID:      id,
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

	// A read stopped early by a break leaves the body unread, which is not an
	// error — only a genuine read failure is.
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("error reading logs: %w", err)
	}

	return &output, nil
}
