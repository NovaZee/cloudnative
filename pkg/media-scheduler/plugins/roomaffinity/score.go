// Package roomaffinity implements a score plugin for room-based pod affinity.
// Pods with the same room label/annotation prefer to be scheduled on the same node.
package roomaffinity

import (
	"context"
	"fmt"

	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/kubernetes/pkg/scheduler/framework"
)

const (
	// Name is the plugin name.
	Name = "RoomAffinity"
	// RoomLabelKey is the pod label key for room specification.
	RoomLabelKey = "media.scheduler/room"
	// RoomAnnotationKey is the pod annotation key for room specification (alternative to label).
	RoomAnnotationKey = "media.scheduler/room"
	// NodeRoomLabelKey is the node label key that stores the room ID.
	NodeRoomLabelKey = "media.scheduler/room-id"
	// MaxScore is the maximum score a node can receive.
	MaxScore = int64(100)
	// MinScore is the minimum score a node can receive.
	MinScore = int64(0)
)

// RoomAffinity scores nodes based on room affinity with the pod.
type RoomAffinity struct {
	handle framework.Handle
}

// New creates a new RoomAffinity plugin.
func New(obj runtime.Object, h framework.Handle) (framework.Plugin, error) {
	return &RoomAffinity{
		handle: h,
	}, nil
}

// Name returns the plugin name.
func (s *RoomAffinity) Name() string {
	return Name
}

// Score assigns a score to each node based on room affinity.
// - Nodes in the same room as the pod receive MaxScore (100).
// - Nodes in a different room receive MinScore (0).
// - Nodes without room label receive a neutral score (50).
// - Pods without room specification are scored neutrally for all nodes (50).
func (s *RoomAffinity) Score(ctx context.Context, state *framework.CycleState, pod *v1.Pod, nodeName string) (int64, *framework.Status) {
	// Get the pod's room from label or annotation.
	podRoom, ok := getPodRoom(pod)
	if !ok {
		// Pod has no room requirement, all nodes are equally suitable.
		return MaxScore / 2, framework.NewStatus(framework.Success)
	}

	// Get node info to check its room label.
	nodeInfo, err := s.handle.SnapshotSharedLister().NodeInfos().Get(nodeName)
	if err != nil {
		return 0, framework.NewStatus(framework.Error, fmt.Sprintf("getting node info: %v", err))
	}

	node := nodeInfo.Node()
	if node == nil {
		return 0, framework.NewStatus(framework.Error, "node not found")
	}

	// Get the node's room from labels.
	nodeRoom, ok := node.Labels[NodeRoomLabelKey]
	if !ok {
		// Node has no room label, give neutral score.
		return MaxScore / 2, framework.NewStatus(framework.Success)
	}

	// Score based on room match.
	if nodeRoom == podRoom {
		return MaxScore, framework.NewStatus(framework.Success)
	}
	return MinScore, framework.NewStatus(framework.Success)
}

// ScoreExtensions returns the score extensions for this plugin.
func (s *RoomAffinity) ScoreExtensions() framework.ScoreExtensions {
	return s
}

// NormalizeScore normalizes scores to the range [MinScore, MaxScore].
// This implementation ensures scores are already in the correct range.
func (s *RoomAffinity) NormalizeScore(ctx context.Context, state *framework.CycleState, pod *v1.Pod, scores framework.NodeScoreList) *framework.Status {
	// Scores are already normalized in the Score method.
	// Just validate they are within bounds.
	for i := range scores {
		if scores[i].Score < MinScore {
			scores[i].Score = MinScore
		}
		if scores[i].Score > MaxScore {
			scores[i].Score = MaxScore
		}
	}
	return nil
}

// getPodRoom extracts the room ID from a pod's labels or annotations.
// Label takes precedence over annotation.
// Returns the room ID and true if found, empty string and false otherwise.
func getPodRoom(pod *v1.Pod) (string, bool) {
	// Check label first.
	if room, ok := pod.Labels[RoomLabelKey]; ok && room != "" {
		return room, true
	}

	// Fall back to annotation.
	if room, ok := pod.Annotations[RoomAnnotationKey]; ok && room != "" {
		return room, true
	}

	return "", false
}