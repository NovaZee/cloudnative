package bandwidth

import (
	"context"
	v1 "k8s.io/api/core/v1"
	"k8s.io/kubernetes/pkg/scheduler/framework"
	"strconv"
)

const (
	Name = "BandwidthFilter"
)

type MediaBandwidthFilter struct {
	minBandwidth int64
}

func (f *MediaBandwidthFilter) Name() string { return Name }

func (f *MediaBandwidthFilter) Filter(ctx context.Context, state *framework.CycleState, pod *v1.Pod, nodeInfo *framework.NodeInfo) *framework.Status {
	node := nodeInfo.Node()
	bwStr := node.Labels["net.bandwidth"]

	bw, _ := strconv.ParseInt(bwStr, 10, 64)

	if bw < f.minBandwidth {
		return framework.NewStatus(framework.Unschedulable, "bandwidth not enough")
	}
	return framework.NewStatus(framework.Success)
}
