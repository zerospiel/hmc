package tracker

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/segmentio/analytics-go/v3"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/tools/clientcmd"
	kubevirtv1 "kubevirt.io/api/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	kcmv1 "github.com/K0rdent/kcm/api/v1beta1"
	"github.com/K0rdent/kcm/internal/utils/pointer"
)

type SegmentIO struct {
	segmentCl       analytics.Client
	crCl            client.Client
	buffer          []SegmentEvent
	systemNamespace string
}

type SegmentEvent struct {
	AnonymousID string         `json:"anonymousId"`
	Event       string         `json:"event"`
	Properties  map[string]any `json:"properties"`
	Timestamp   string         `json:"timestamp"`
}

func NewSegmetIO(systemNamespace string, segmentClient analytics.Client, crClient client.Client) *SegmentIO {
	return &SegmentIO{
		segmentCl:       segmentClient,
		crCl:            crClient,
		systemNamespace: systemNamespace,
	}
}

// wrap in retries all of the get/list calls
// TODO: wrap lists in limit-continue
func (t *SegmentIO) Collect(ctx context.Context) error {
	// TODO: these steps are to be wrapped
	mgmt := &kcmv1.Management{}
	if err := t.crCl.Get(ctx, client.ObjectKey{Name: kcmv1.ManagementName}, mgmt); err != nil {
		return fmt.Errorf("failed to get Management: %w", err)
	}

	mgmtID := string(mgmt.UID)

	templatesList := &kcmv1.ClusterTemplateList{}
	if err := t.crCl.List(ctx, templatesList, client.InNamespace(t.systemNamespace)); err != nil {
		return fmt.Errorf("failed to list ClusterTemplates: %w", err)
	}

	templates := make(map[string]kcmv1.ClusterTemplate)
	for _, template := range templatesList.Items {
		templates[template.Name] = template
	}

	clusterDeployments := &kcmv1.ClusterDeploymentList{}
	if err := t.crCl.List(ctx, clusterDeployments); err != nil {
		return fmt.Errorf("failed to list ClusterDeployments: %w", err)
	}

	// TODO: process in batches?
	for _, cld := range clusterDeployments.Items {
		template, ok := templates[cld.Spec.Template]
		if !ok {
			continue
		}
		_ = template
		// TODO: get child clsuter client
		// collect
		// enqueuq

		if err := t.segmentCl.Enqueue(analytics.Track{
			AnonymousId: mgmtID,
			Event:       "child-cluster-heartbeat",
			Properties:  nil,
		}); err != nil {
			// TODO: LOG error
			continue
		}
	}

	return nil
}

func getChildClient(ctx context.Context, cl client.Client, clusterName, systemNamespace string, scheme *runtime.Scheme) (client.Client, error) {
	secret := new(corev1.Secret)
	if err := cl.Get(ctx, client.ObjectKey{Name: clusterName + "-kubeconfig", Namespace: systemNamespace}, secret); err != nil {
		return nil, fmt.Errorf("failed to get Secret with kubeconfig: %w", err)
	}

	data, ok := secret.Data["value"]
	if !ok { // sanity check
		return nil, fmt.Errorf("kubeconfig from Secret %s is empty", client.ObjectKeyFromObject(secret))
	}

	apicfg, err := clientcmd.Load(data)
	if err != nil {
		return nil, fmt.Errorf("failed to load kubeconfig from %s Secret: %w", client.ObjectKeyFromObject(secret), err)
	}
	restcfg, err := clientcmd.NewDefaultClientConfig(*apicfg, &clientcmd.ConfigOverrides{}).ClientConfig()
	if err != nil {
		return nil, fmt.Errorf("failed to get client REST config from %s Secret: %w", client.ObjectKeyFromObject(secret), err)
	}

	childCl, err := client.New(restcfg, client.Options{Scheme: scheme})
	if err != nil {
		return nil, fmt.Errorf("failed to create client config from %s Secret: %w", client.ObjectKeyFromObject(secret), err)
	}

	return childCl, nil
}

func collectChildProperties(ctx context.Context, childCl client.Client) (map[string]any, error) {
	// TODO: separate according to the doc: send not only global (nodes) counters, but also per-node
	// this require to send events after collection to separate events correctly
	finalProperties := make(map[string]any)

	nodes, err := paginatedList[*corev1.Node, *corev1.NodeList](ctx, childCl, 100)
	if err != nil {
		return nil, fmt.Errorf("failed to list nodes: %w", err)
	}
	populateNodeMetrics(finalProperties, nodes)

	pods, err := paginatedList[*corev1.Pod, *corev1.PodList](ctx, childCl, 200)
	if err != nil {
		return nil, fmt.Errorf("failed to list pods: %w", err)
	}
	populatePodGpuMetrics(finalProperties, pods)

	vmis, err := paginatedList[*kubevirtv1.VirtualMachineInstance, *kubevirtv1.VirtualMachineInstanceList](ctx, childCl, 100)
	if err == nil {
		finalProperties["kubevirt.vmis"] = len(vmis)
	}

	daemonSets, err := paginatedList[*appsv1.DaemonSet, *appsv1.DaemonSetList](ctx, childCl, 100)
	if err == nil {
		populateGpuOperatorPresence(finalProperties, daemonSets)
	}

	return finalProperties, nil
}

func populateNodeMetrics(result map[string]any, nodes []*corev1.Node) {
	totalCPU, totalMemory := int64(0), int64(0)
	totalGPUsNVIDIA, totalGPUsAMD, gpuNodes := int64(0), int64(0), 0

	var (
		archCnt         = make(map[string]int)
		osCnt           = make(map[string]int)
		kernelCnt       = make(map[string]int)
		kubeFlavorsCnt  = make(map[string]int)
		kubeVersionsCnt = make(map[string]int)
	)

	for _, node := range nodes {
		totalCPU += node.Status.Capacity.Cpu().MilliValue() / 1000
		totalMemory += node.Status.Capacity.Memory().Value()

		gpusN := pointer.To(node.Status.Capacity["nvidia.com/gpu"]).Value()
		gpusA := pointer.To(node.Status.Capacity["amd.com/gpu"]).Value()
		if gpusN > 0 || gpusA > 0 {
			gpuNodes++
		}
		totalGPUsNVIDIA += gpusN
		totalGPUsAMD += gpusA

		info := node.Status.NodeInfo
		archCnt[info.Architecture]++
		osCnt[info.OSImage]++
		kernelCnt[info.KernelVersion]++

		if idx := strings.Index(info.KubeletVersion, "+"); idx >= 0 {
			kubeVersionsCnt[info.KubeletVersion[:idx]]++
			kubeFlavorsCnt[info.KernelVersion[idx+1:]]++
		} else {
			kubeVersionsCnt[info.KubeletVersion]++
		}
	}

	result["node.count"] = len(nodes)
	result["node.cpu.total"] = totalCPU
	result["node.memory.total_bytes"] = totalMemory

	result["node.gpu.nvidia.total"] = totalGPUsNVIDIA
	result["node.gpu.amd.total"] = totalGPUsAMD
	result["node.gpu.nodes_with_gpu"] = gpuNodes

	result["node.arch"] = archCnt
	result["node.os"] = osCnt
	result["node.kernel"] = kernelCnt

	result["node.k8s_versions"] = kubeVersionsCnt
	result["node.kernel_flavors"] = kubeFlavorsCnt
}

func populatePodGpuMetrics(result map[string]any, pods []*corev1.Pod) {
	gpuPods := 0
	gpuInUseNVIDIA := int64(0)
	gpuInUseAMD := int64(0)

	for _, pod := range pods {
		for _, container := range pod.Spec.Containers {
			nReq := pointer.To(container.Resources.Requests["nvidia.com/gpu"]).Value()
			aReq := pointer.To(container.Resources.Requests["amd.com/gpu"]).Value()
			if nReq > 0 || aReq > 0 {
				gpuPods++
				gpuInUseNVIDIA += nReq
				gpuInUseAMD += aReq
			}
		}
	}

	result["pods.with_gpu_requests"] = gpuPods
	result["pods.gpu_in_use.nvidia"] = gpuInUseNVIDIA
	result["pods.gpu_in_use.amd"] = gpuInUseAMD
}

func populateGpuOperatorPresence(result map[string]any, dsList []*appsv1.DaemonSet) {
	gpuOperators := map[string]bool{
		"nvidia": false,
		"amd":    false,
	}

	for _, ds := range dsList {
		name := strings.ToLower(ds.Name)
		if strings.Contains(name, "nvidia") {
			gpuOperators["nvidia"] = true
		}
		if strings.Contains(name, "amd") {
			gpuOperators["amd"] = true
		}
	}

	result["gpu.operator.nvidia"] = gpuOperators["nvidia"]
	result["gpu.operator.amd"] = gpuOperators["amd"]
}

func paginatedList[T client.Object, L client.ObjectList](ctx context.Context, c client.Client, limit int64) ([]T, error) {
	var (
		result []T
		cont   string

		opts = &client.ListOptions{Limit: limit}
	)

	for {
		listObj := newList[L]()

		opts.Continue = cont
		if err := c.List(ctx, listObj, opts); err != nil {
			return nil, fmt.Errorf("failed to list %T: %w", listObj, err)
		}

		slice, err := extractTypedItems[T](listObj)
		if err != nil {
			return nil, fmt.Errorf("extracting items: %w", err)
		}
		result = append(result, slice...)

		cont = listObj.GetContinue()
		if cont == "" {
			break
		}
	}

	return result, nil
}

func extractTypedItems[T client.Object](list client.ObjectList) ([]T, error) {
	switch l := list.(type) {
	case *corev1.PodList:
		out := make([]T, len(l.Items))
		for i := range l.Items {
			out[i] = any(&l.Items[i]).(T)
		}
		return out, nil
	case *corev1.NodeList:
		out := make([]T, len(l.Items))
		for i := range l.Items {
			out[i] = any(&l.Items[i]).(T)
		}
		return out, nil
	case *appsv1.DaemonSetList:
		out := make([]T, len(l.Items))
		for i := range l.Items {
			out[i] = any(&l.Items[i]).(T)
		}
		return out, nil
	case *kubevirtv1.VirtualMachineInstanceList:
		out := make([]T, len(l.Items))
		for i := range l.Items {
			out[i] = any(&l.Items[i]).(T)
		}
		return out, nil
	default:
		return nil, fmt.Errorf("unsupported list type: %T", list)
	}
}

func newList[T client.ObjectList]() T { return *(new(T)) }

// TODO: to remove
func (t *SegmentIO) Collect1(ctx context.Context, cluster string, collector Collector) error {
	data, err := collector.CollectFromCluster(ctx, cluster)
	if err != nil {
		return fmt.Errorf("failed to collect metrics from cluster %q: %w", cluster, err)
	}

	event := SegmentEvent{
		AnonymousID: uuid.New().String(),
		Event:       "ClusterTelemetry",
		Timestamp:   time.Now().UTC().Format(time.RFC3339),
		Properties: map[string]any{
			"cluster": cluster,
			"metrics": data,
		},
	}
	t.buffer = append(t.buffer, event)
	return nil
}

func (t *SegmentIO) Flush(ctx context.Context) error {
	if len(t.buffer) == 0 {
		return nil
	}
	events := t.buffer
	t.buffer = nil

	// TODO: no need
	payload := map[string]any{
		"batch": events,
	}
	jsonBody, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("marshal payload: %w", err)
	}
	_ = jsonBody

	// retryPolicy := backoff.WithMaxRetries(backoff.NewExponentialBackOff(), uint64(o.retryLimit))

	// send := func() error {
	// 	req, err := http.NewRequestWithContext(ctx, http.MethodPost, o.endpoint, bytes.NewReader(jsonBody))
	// 	if err != nil {
	// 		return err
	// 	}
	// 	req.Header.Set("Content-Type", "application/json")
	// 	req.SetBasicAuth(o.writeKey, "")
	// 	resp, err := o.client.Do(req)
	// 	if err != nil {
	// 		return err
	// 	}
	// 	defer resp.Body.Close() //nolint:errcheck // no need

	// 	if resp.StatusCode >= 300 {
	// 		return fmt.Errorf("segment returned status %d", resp.StatusCode)
	// 	}
	// 	return nil
	// }

	// if err := backoff.Retry(send, retryPolicy); err != nil {
	// 	return fmt.Errorf("failed to send telemetry after retries: %w", err)
	// }
	return nil
}

func (t *SegmentIO) Close(ctx context.Context) error {
	return t.Flush(ctx)
}
