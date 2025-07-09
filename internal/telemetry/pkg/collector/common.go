package collector

import (
	"context"
	"strings"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
	kubevirtv1 "kubevirt.io/api/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/K0rdent/kcm/internal/utils/pointer"
)

type CommonCollector struct {
	Client client.Client
	Scheme *runtime.Scheme
}

func (c *CommonCollector) CollectFromCluster(ctx context.Context, _ string) (map[string]any, error) {
	result := make(map[string]any)

	var nodes corev1.NodeList
	if err := c.Client.List(ctx, &nodes); err != nil {
		return nil, err
	}

	totalCPU := int64(0)
	totalMemory := int64(0)
	totalGPUsNVIDIA := int64(0)
	totalGPUsAMD := int64(0)
	gpuNodes := 0

	archMap := make(map[string]int)
	osMap := make(map[string]int)
	kernelMap := make(map[string]int)
	flavors := make(map[string]int)
	k8sVersions := make(map[string]int)

	for _, node := range nodes.Items {
		cpu := node.Status.Capacity.Cpu().MilliValue() / 1000
		mem := node.Status.Capacity.Memory().Value()
		totalCPU += cpu
		totalMemory += mem

		gpusN := node.Status.Capacity["nvidia.com/gpu"]
		gpusA := node.Status.Capacity["amd.com/gpu"]
		n := gpusN.Value()
		a := gpusA.Value()
		if n+a > 0 {
			gpuNodes++
		}
		totalGPUsNVIDIA += n
		totalGPUsAMD += a

		info := node.Status.NodeInfo
		archMap[info.Architecture]++
		osMap[info.OSImage]++
		kernelMap[info.KernelVersion]++

		k8sVersions[info.KubeletVersion]++

		if idx := strings.Index(info.KernelVersion, "+"); idx >= 0 {
			flavor := info.KernelVersion[idx+1:]
			flavors[flavor]++
		}
	}

	result["node.count"] = len(nodes.Items)
	result["node.cpu.total"] = totalCPU
	result["node.memory.total_bytes"] = totalMemory
	result["node.gpu.nvidia.total"] = totalGPUsNVIDIA
	result["node.gpu.amd.total"] = totalGPUsAMD
	result["node.gpu.nodes_with_gpu"] = gpuNodes
	result["node.arch"] = archMap
	result["node.os"] = osMap
	result["node.kernel"] = kernelMap
	result["node.k8s_versions"] = k8sVersions
	result["node.kernel_flavors"] = flavors

	// Pod-level GPU usage
	var pods corev1.PodList
	if err := c.Client.List(ctx, &pods); err != nil {
		return nil, err
	}

	gpuPods := 0
	gpuInUseNVIDIA := int64(0)
	gpuInUseAMD := int64(0)
	for _, pod := range pods.Items {
		for _, container := range pod.Spec.Containers {
			nReq := pointer.To(container.Resources.Requests["nvidia.com/gpu"]).Value()
			aReq := pointer.To(container.Resources.Requests["amd.com/gpu"]).Value()
			if nReq+aReq > 0 {
				gpuPods++
				gpuInUseNVIDIA += nReq
				gpuInUseAMD += aReq
			}
		}
	}
	result["pods.with_gpu_requests"] = gpuPods
	result["pods.gpu_in_use.nvidia"] = gpuInUseNVIDIA
	result["pods.gpu_in_use.amd"] = gpuInUseAMD

	// KubeVirt VMs
	var vmis kubevirtv1.VirtualMachineInstanceList
	_ = c.Client.List(ctx, &vmis, &client.ListOptions{LabelSelector: labels.Everything()})
	result["kubevirt.vmis"] = len(vmis.Items)

	// GPU Operators presence (heuristic)
	var dsList appsv1.DaemonSetList
	_ = c.Client.List(ctx, &dsList)
	gpuOperators := map[string]bool{
		"nvidia": false,
		"amd":    false,
	}
	for _, ds := range dsList.Items {
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

	return result, nil
}
