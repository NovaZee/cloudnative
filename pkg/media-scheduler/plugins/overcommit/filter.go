// Package overcommit implements a filter plugin for resource oversubscription scheduling.
// It filters nodes that have insufficient overcommit capacity for requested resources.
package overcommit

import (
	"context"
	"fmt"
	"k8s.io/klog/v2"
	"strings"

	cloudnative "cloudnative/pkg/media-scheduler/pkg/overcommit"
	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/kubernetes/pkg/scheduler/framework"
)

const (
	// Name is the plugin name.
	Name = "OvercommitFilter"
)

// OvercommitFilter filters nodes based on overcommit resource availability.
type OvercommitFilter struct {
	handle     framework.Handle
	overcommit *cloudnative.Manager
}

// New creates a new OvercommitFilter plugin.
func New(obj runtime.Object, h framework.Handle) (framework.Plugin, error) {
	// Get Kubernetes client from handle.
	client := h.ClientSet()

	// Create overcommit manager with default ConfigMap settings.
	overcommitMgr := cloudnative.NewManager(client)

	// Load initial configuration from ConfigMap.
	ctx := context.Background()
	if err := overcommitMgr.Load(ctx); err != nil {
		return nil, fmt.Errorf("failed to load overcommit config: %w", err)
	}

	return &OvercommitFilter{
		handle:     h,
		overcommit: overcommitMgr,
	}, nil
}

// Name returns the plugin name.
func (f *OvercommitFilter) Name() string {
	return Name
}

// Filter checks if a node has enough overcommit capacity for the pod's resource requests.
// Nodes without sufficient capacity are marked as unschedulable.
func (f *OvercommitFilter) Filter(ctx context.Context, state *framework.CycleState, pod *v1.Pod, nodeInfo *framework.NodeInfo) *framework.Status {
	node := nodeInfo.Node()
	if node == nil {
		return framework.NewStatus(framework.Error, "node not found")
	}
	klog.V(5).Infof("OvercommitFilter: checking node %s for pod %s/%s", node.Name, pod.Namespace, pod.Name)
	// Get pod's resource requests.
	requests := getResourceRequests(pod)
	if len(requests) == 0 {
		// No resource requests, skip filtering.
		return framework.NewStatus(framework.Success)
	}
	// Check each requested resource against overcommit capacity.

	for resourceType, reqQty := range requests {
		ratio := f.overcommit.GetRatio(string(resourceType))
		if ratio <= 1.0 {
			klog.V(5).Infof("OvercommitFilter: overcommit ratio is %f%%", ratio)
			continue
		}

		allocated := f.overcommit.GetAllocated(node.Name, string(resourceType))

		// Calculate effective capacity: request * overcommit ratio.
		maxAllowed := int64(float64(reqQty.MilliValue()) * ratio)

		// Check if current allocation + new request would exceed capacity.
		newTotal := allocated.MilliValue() + reqQty.MilliValue()
		klog.V(5).Infof("==========OvercommitFilter: node %s, resource %s, allocated=%d, requesting=%d, ratio=%.2f, maxAllowed=%d, newTotal=%d",
			node.Name, resourceType, allocated.MilliValue(), reqQty.MilliValue(), ratio, maxAllowed, newTotal)
		if newTotal > maxAllowed {
			return framework.NewStatus(framework.Unschedulable,
				fmt.Sprintf("overcommit capacity exceeded for %s on node %s: allocated=%d, requesting=%d, ratio=%.2f, max=%d",
					resourceType, node.Name, allocated.MilliValue(), reqQty.MilliValue(), ratio, maxAllowed))
		}
	}

	return framework.NewStatus(framework.Success)
}

// getResourceRequests extracts extended resource requests from a pod.
// Returns only extended resources (custom resources) for overcommit consideration.
func getResourceRequests(pod *v1.Pod) v1.ResourceList {
	requests := v1.ResourceList{}

	// Collect container requests.
	for _, container := range pod.Spec.Containers {
		for name, qty := range container.Resources.Requests {
			// Only consider extended resources (custom resources).
			// Skip built-in resources like cpu, memory.
			if !isBuiltInResource(v1.ResourceName(name)) {
				if existing, exists := requests[name]; exists {
					existing.Add(qty)
					requests[name] = existing
				} else {
					requests[name] = qty
				}
			}
		}
	}

	return requests
}

// isBuiltInResource checks if a resource is a built-in Kubernetes resource.
func isBuiltInResource(name v1.ResourceName) bool {
	switch name {
	case v1.ResourceCPU, v1.ResourceMemory, v1.ResourceStorage,
		v1.ResourceEphemeralStorage, v1.ResourceRequestsCPU,
		v1.ResourceRequestsMemory, v1.ResourceRequestsStorage,
		v1.ResourceLimitsCPU, v1.ResourceLimitsMemory,
		v1.ResourcePods, v1.ResourceHugePagesPrefix:
		return true
	default:
		// Check for hugepages resources (e.g., hugepages-2Mi)
		if isHugePagesResource(name) {
			return true
		}
		return false
	}
}

// isHugePagesResource checks if a resource is a hugepages resource.
func isHugePagesResource(name v1.ResourceName) bool {
	return strings.HasPrefix(string(name), "hugepages-")
}
