// Package overcommit implements resource overcommit logic for media scheduler.
// It manages oversubscription ratios and tracks resource allocation via ConfigMap.
package overcommit

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
)

const (
	// DefaultConfigMapName is the default ConfigMap name for storing overcommit resources.
	DefaultConfigMapName = "media-scheduler-overcommit"
	// DefaultConfigMapNamespace is the default namespace for the ConfigMap.
	DefaultConfigMapNamespace = "kube-system"
	// OvercommitRatioKey is the key in ConfigMap data for overcommit configuration.
	OvercommitRatioKey = "overcommit-ratios"
	// NodeResourcesKey is the key in ConfigMap data for node resources.
	NodeResourcesKey = "node-resources"
)

// OvercommitRatio defines the oversubscription ratio for a resource type.
type OvercommitRatio struct {
	// ResourceName is the name of the resource (e.g., "nvidia.com/gpu", "bandwidth").
	ResourceName string `json:"resourceName"`
	// Ratio is the oversubscription ratio (e.g., 1.5 means 150% allocation).
	Ratio float64 `json:"ratio"`
}

// NodeResourceAllocated tracks allocated resources for a specific node.
type NodeResourceAllocated struct {
	// NodeName is the name of the node.
	NodeName string `json:"nodeName"`
	// Allocated is the map of allocated resources per resource type.
	Allocated map[string]string `json:"allocated"`
}

// OvercommitConfig holds the overcommit configuration data from ConfigMap.
type OvercommitConfig struct {
	// Ratios defines the oversubscription ratios for each resource type.
	Ratios []OvercommitRatio `json:"ratios"`
	// NodeResources tracks allocated resources per node.
	NodeResources map[string]NodeResourceAllocated `json:"nodeResources"`
}

// Manager manages overcommit resource state synchronized via ConfigMap.
type Manager struct {
	client          kubernetes.Interface
	configMapName   string
	configMapNS     string
	mu              sync.RWMutex
	currentConfig   *OvercommitConfig
}

// NewManager creates a new overcommit resource manager.
func NewManager(client kubernetes.Interface, opts ...Option) *Manager {
	m := &Manager{
		client:        client,
		configMapName: DefaultConfigMapName,
		configMapNS:   DefaultConfigMapNamespace,
		currentConfig: &OvercommitConfig{
			Ratios:        make([]OvercommitRatio, 0),
			NodeResources: make(map[string]NodeResourceAllocated),
		},
	}

	for _, opt := range opts {
		opt(m)
	}

	return m
}

// Option configures the Manager.
type Option func(*Manager)

// WithConfigMap configures the ConfigMap name and namespace.
func WithConfigMap(name, namespace string) Option {
	return func(m *Manager) {
		m.configMapName = name
		m.configMapNS = namespace
	}
}

// Load loads the overcommit configuration from ConfigMap.
func (m *Manager) Load(ctx context.Context) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	cm, err := m.client.CoreV1().ConfigMaps(m.configMapNS).Get(ctx, m.configMapName, metav1.GetOptions{})
	if err != nil {
		// ConfigMap not found is acceptable for first run, initialize with empty config.
		return m.initConfigMap(ctx)
	}

	configJSON := cm.Data[OvercommitRatioKey]
	if configJSON == "" {
		return fmt.Errorf("missing %s key in ConfigMap", OvercommitRatioKey)
	}

	var config OvercommitConfig
	if err := json.Unmarshal([]byte(configJSON), &config); err != nil {
		return fmt.Errorf("failed to parse overcommit config: %w", err)
	}

	m.currentConfig = &config
	return nil
}

// initConfigMap creates a new ConfigMap with default configuration.
func (m *Manager) initConfigMap(ctx context.Context) error {
	cm := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      m.configMapName,
			Namespace: m.configMapNS,
		},
		Data: map[string]string{
			OvercommitRatioKey: "{}",
		},
	}

	_, err := m.client.CoreV1().ConfigMaps(m.configMapNS).Create(ctx, cm, metav1.CreateOptions{})
	return err
}

// GetRatio returns the overcommit ratio for the given resource type.
// Returns 1.0 (no overcommit) if not configured.
func (m *Manager) GetRatio(resourceType string) float64 {
	m.mu.RLock()
	defer m.mu.RUnlock()

	for _, r := range m.currentConfig.Ratios {
		if r.ResourceName == resourceType {
			return r.Ratio
		}
	}
	return 1.0
}

// GetAllocated returns the allocated resource quantity for a node and resource type.
func (m *Manager) GetAllocated(nodeName, resourceType string) resource.Quantity {
	m.mu.RLock()
	defer m.mu.RUnlock()

	nodeRes, exists := m.currentConfig.NodeResources[nodeName]
	if !exists {
		return resource.MustParse("0")
	}

	allocatedStr, exists := nodeRes.Allocated[resourceType]
	if !exists {
		return resource.MustParse("0")
	}

	q, err := resource.ParseQuantity(allocatedStr)
	if err != nil {
		return resource.MustParse("0")
	}
	return q
}

// Allocate attempts to allocate resources for a pod on a node.
// Returns true if allocation succeeds, false otherwise.
func (m *Manager) Allocate(ctx context.Context, nodeName string, requests corev1.ResourceList) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	// Check if allocation is possible for each resource.
	for resourceType, reqQty := range requests {
		ratio := m.getRatioUnsafe(string(resourceType))
		if ratio <= 0 {
			continue
		}

		allocated := m.getAllocatedUnsafe(nodeName, string(resourceType))
		// Effective capacity = requested * ratio
		maxAllowed := resource.NewQuantity(int64(float64(reqQty.MilliValue())*ratio), resource.DecimalSI)

		// Check if adding new allocation would exceed effective capacity.
		newTotal := allocated.DeepCopy()
		newTotal.Add(reqQty)

		if newTotal.Cmp(*maxAllowed) > 0 {
			return false, nil
		}
	}

	// Update allocations.
	if m.currentConfig.NodeResources == nil {
		m.currentConfig.NodeResources = make(map[string]NodeResourceAllocated)
	}

	nodeRes := m.currentConfig.NodeResources[nodeName]
	if nodeRes.Allocated == nil {
		nodeRes.Allocated = make(map[string]string)
		nodeRes.NodeName = nodeName
	}

	for resourceType, reqQty := range requests {
		current := nodeRes.Allocated[string(resourceType)]
		currQty, _ := resource.ParseQuantity(current)
		currQty.Add(reqQty)
		nodeRes.Allocated[string(resourceType)] = currQty.String()
	}

	m.currentConfig.NodeResources[nodeName] = nodeRes

	if err := m.saveConfig(ctx); err != nil {
		return false, err
	}
	return true, nil
}

// Release releases allocated resources for a pod from a node.
func (m *Manager) Release(ctx context.Context, nodeName string, requests corev1.ResourceList) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	nodeRes, exists := m.currentConfig.NodeResources[nodeName]
	if !exists {
		return nil
	}

	for resourceType, reqQty := range requests {
		current := nodeRes.Allocated[string(resourceType)]
		currQty, err := resource.ParseQuantity(current)
		if err != nil {
			continue
		}

		currQty.Sub(reqQty)
		if currQty.IsZero() {
			delete(nodeRes.Allocated, string(resourceType))
		} else {
			nodeRes.Allocated[string(resourceType)] = currQty.String()
		}
	}

	m.currentConfig.NodeResources[nodeName] = nodeRes
	return m.saveConfig(ctx)
}

// getRatioUnsafe returns ratio without lock (caller must hold lock).
func (m *Manager) getRatioUnsafe(resourceType string) float64 {
	for _, r := range m.currentConfig.Ratios {
		if r.ResourceName == resourceType {
			return r.Ratio
		}
	}
	return 1.0
}

// getAllocatedUnsafe returns allocated quantity without lock (caller must hold lock).
func (m *Manager) getAllocatedUnsafe(nodeName, resourceType string) resource.Quantity {
	nodeRes, exists := m.currentConfig.NodeResources[nodeName]
	if !exists {
		return resource.MustParse("0")
	}

	allocatedStr, exists := nodeRes.Allocated[resourceType]
	if !exists {
		return resource.MustParse("0")
	}

	q, err := resource.ParseQuantity(allocatedStr)
	if err != nil {
		return resource.MustParse("0")
	}
	return q
}

// saveConfig saves the current configuration to ConfigMap.
func (m *Manager) saveConfig(ctx context.Context) error {
	configJSON, err := json.Marshal(m.currentConfig)
	if err != nil {
		return fmt.Errorf("failed to marshal config: %w", err)
	}

	cm, err := m.client.CoreV1().ConfigMaps(m.configMapNS).Get(ctx, m.configMapName, metav1.GetOptions{})
	if err != nil {
		return err
	}

	cm.Data[OvercommitRatioKey] = string(configJSON)

	_, err = m.client.CoreV1().ConfigMaps(m.configMapNS).Update(ctx, cm, metav1.UpdateOptions{})
	return err
}