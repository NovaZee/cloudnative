package driver

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"

	csi "github.com/container-storage-interface/spec/lib/go/csi"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"k8s.io/klog/v2"

	"cloudnative/pkg/localpv/pkg/mounter"
	"cloudnative/pkg/localpv/pkg/overprovision"
	"cloudnative/pkg/localpv/pkg/state"
)

const (
	DefaultDriverName = "localpv.csi.cloudnative.io"
	DefaultVersion    = "0.1.0"

	// Use a custom topology key so kubelet can safely label the node on driver registration.
	// Avoid kubernetes.io/k8s.io/topology.kubernetes.io namespaces because they are often restricted by NodeRestriction.
	TopologyKeyNode = "localpv.csi.cloudnative.io/node"

	VolumeContextPathKey = "localpv.csi.cloudnative.io/path"
	VolumeContextPoolKey = "localpv.csi.cloudnative.io/pool"
)

type Options struct {
	Name       string
	Version    string
	NodeID     string
	BaseDir    string
	VolumesDir string

	State    state.Store
	Overprov overprovision.Provider
}

type Driver struct {
	csi.UnimplementedIdentityServer
	csi.UnimplementedControllerServer
	csi.UnimplementedNodeServer

	name    string
	version string
	nodeID  string

	baseDir    string
	volumesDir string

	state    state.Store
	overprov overprovision.Provider
	mounter  mounter.Interface

	mu sync.Mutex
}

func New(opts Options) (*Driver, error) {
	if opts.Name == "" {
		opts.Name = DefaultDriverName
	}
	if opts.Version == "" {
		opts.Version = DefaultVersion
	}
	if opts.NodeID == "" {
		return nil, fmt.Errorf("nodeID is required")
	}
	if opts.BaseDir == "" {
		return nil, fmt.Errorf("baseDir is required")
	}
	if opts.VolumesDir == "" {
		opts.VolumesDir = DefaultVolumesDir(opts.BaseDir)
	}
	if opts.State == nil {
		return nil, fmt.Errorf("state store is required")
	}
	if opts.Overprov == nil {
		opts.Overprov = overprovision.NewStaticProvider(overprovision.Config{DefaultOverprovisionRatio: 1.0})
	}

	mnt, err := mounter.New()
	if err != nil {
		return nil, fmt.Errorf("init mounter: %w", err)
	}

	return &Driver{
		name:       opts.Name,
		version:    opts.Version,
		nodeID:     opts.NodeID,
		baseDir:    opts.BaseDir,
		volumesDir: opts.VolumesDir,
		state:      opts.State,
		overprov:   opts.Overprov,
		mounter:    mnt,
	}, nil
}

func DefaultVolumesDir(baseDir string) string {
	return filepath.Join(baseDir, "volumes")
}

func DefaultStateFile(baseDir string) string {
	return filepath.Join(baseDir, ".localpv-state.json")
}

// =========================
// IdentityServer
// =========================

func (d *Driver) GetPluginInfo(ctx context.Context, req *csi.GetPluginInfoRequest) (*csi.GetPluginInfoResponse, error) {
	return &csi.GetPluginInfoResponse{
		Name:          d.name,
		VendorVersion: d.version,
	}, nil
}

func (d *Driver) GetPluginCapabilities(ctx context.Context, req *csi.GetPluginCapabilitiesRequest) (*csi.GetPluginCapabilitiesResponse, error) {
	return &csi.GetPluginCapabilitiesResponse{
		Capabilities: []*csi.PluginCapability{
			{
				Type: &csi.PluginCapability_Service_{
					Service: &csi.PluginCapability_Service{
						Type: csi.PluginCapability_Service_CONTROLLER_SERVICE,
					},
				},
			},
			{
				Type: &csi.PluginCapability_Service_{
					Service: &csi.PluginCapability_Service{
						Type: csi.PluginCapability_Service_VOLUME_ACCESSIBILITY_CONSTRAINTS,
					},
				},
			},
		},
	}, nil
}

func (d *Driver) Probe(ctx context.Context, req *csi.ProbeRequest) (*csi.ProbeResponse, error) {
	return &csi.ProbeResponse{}, nil
}

// =========================
// ControllerServer
// =========================

func (d *Driver) ControllerGetCapabilities(ctx context.Context, req *csi.ControllerGetCapabilitiesRequest) (*csi.ControllerGetCapabilitiesResponse, error) {
	return &csi.ControllerGetCapabilitiesResponse{
		Capabilities: []*csi.ControllerServiceCapability{
			{
				Type: &csi.ControllerServiceCapability_Rpc{
					Rpc: &csi.ControllerServiceCapability_RPC{
						Type: csi.ControllerServiceCapability_RPC_CREATE_DELETE_VOLUME,
					},
				},
			},
		},
	}, nil
}

func (d *Driver) CreateVolume(ctx context.Context, req *csi.CreateVolumeRequest) (*csi.CreateVolumeResponse, error) {
	if req.GetName() == "" {
		return nil, status.Error(codes.InvalidArgument, "missing name")
	}
	if len(req.GetVolumeCapabilities()) == 0 {
		return nil, status.Error(codes.InvalidArgument, "missing volume capabilities")
	}

	if ar := req.GetAccessibilityRequirements(); ar != nil {
		if !topologyAllowsNode(ar, d.nodeID) {
			return nil, status.Errorf(codes.FailedPrecondition, "volume is not accessible on node %q", d.nodeID)
		}
	}

	volID := sanitizeID(req.GetName())
	size := int64(0)
	if cr := req.GetCapacityRange(); cr != nil {
		if cr.GetRequiredBytes() > 0 {
			size = cr.GetRequiredBytes()
		} else if cr.GetLimitBytes() > 0 {
			size = cr.GetLimitBytes()
		}
	}

	pool := req.GetParameters()[VolumeContextPoolKey]
	if pool == "" {
		pool = "default"
	}

	d.mu.Lock()
	defer d.mu.Unlock()

	if err := os.MkdirAll(d.volumesDir, 0o750); err != nil {
		return nil, status.Errorf(codes.Internal, "create volumes dir: %v", err)
	}

	volPath := filepath.Join(d.volumesDir, volID)

	// Idempotency: if we already have a record for this volume, return it.
	if v, ok := d.state.Get(volID); ok {
		if err := os.MkdirAll(volPath, 0o750); err != nil {
			return nil, status.Errorf(codes.Internal, "ensure volume dir: %v", err)
		}
		existingPool := v.Pool
		if existingPool == "" {
			existingPool = pool
		}
		return d.createVolumeResponse(volID, volPath, v.RequestedBytes, existingPool), nil
	}

	if size <= 0 {
		size = 1 << 20 // 1Mi default when PVC does not request a size
	}

	if err := d.checkCapacityLocked(pool, size); err != nil {
		return nil, err
	}

	if err := os.MkdirAll(volPath, 0o750); err != nil {
		return nil, status.Errorf(codes.Internal, "create volume dir: %v", err)
	}

	if err := d.state.Upsert(state.Volume{
		ID:             volID,
		RequestedBytes: size,
		Pool:           pool,
	}); err != nil {
		return nil, status.Errorf(codes.Internal, "persist volume state: %v", err)
	}

	klog.Infof("CreateVolume: id=%s pool=%s size=%d path=%s node=%s", volID, pool, size, volPath, d.nodeID)
	return d.createVolumeResponse(volID, volPath, size, pool), nil
}

func (d *Driver) DeleteVolume(ctx context.Context, req *csi.DeleteVolumeRequest) (*csi.DeleteVolumeResponse, error) {
	if req.GetVolumeId() == "" {
		return nil, status.Error(codes.InvalidArgument, "missing volume ID")
	}
	volID := sanitizeID(req.GetVolumeId())
	volPath := filepath.Join(d.volumesDir, volID)

	d.mu.Lock()
	defer d.mu.Unlock()

	_ = os.RemoveAll(volPath)
	_ = d.state.Delete(volID)

	klog.Infof("DeleteVolume: id=%s path=%s node=%s", volID, volPath, d.nodeID)
	return &csi.DeleteVolumeResponse{}, nil
}

// =========================
// NodeServer
// =========================

func (d *Driver) NodeGetInfo(ctx context.Context, req *csi.NodeGetInfoRequest) (*csi.NodeGetInfoResponse, error) {
	return &csi.NodeGetInfoResponse{
		NodeId: d.nodeID,
		AccessibleTopology: &csi.Topology{
			Segments: map[string]string{
				TopologyKeyNode: d.nodeID,
			},
		},
	}, nil
}

func (d *Driver) NodeGetCapabilities(ctx context.Context, req *csi.NodeGetCapabilitiesRequest) (*csi.NodeGetCapabilitiesResponse, error) {
	return &csi.NodeGetCapabilitiesResponse{}, nil
}

func (d *Driver) NodePublishVolume(ctx context.Context, req *csi.NodePublishVolumeRequest) (*csi.NodePublishVolumeResponse, error) {
	if req.GetVolumeId() == "" {
		return nil, status.Error(codes.InvalidArgument, "missing volume ID")
	}
	if req.GetTargetPath() == "" {
		return nil, status.Error(codes.InvalidArgument, "missing target path")
	}
	if req.GetVolumeCapability() == nil {
		return nil, status.Error(codes.InvalidArgument, "missing volume capability")
	}
	if req.GetVolumeCapability().GetMount() == nil {
		return nil, status.Error(codes.InvalidArgument, "only mount volumes are supported")
	}

	volID := sanitizeID(req.GetVolumeId())

	source := req.GetVolumeContext()[VolumeContextPathKey]
	if source == "" {
		source = filepath.Join(d.volumesDir, volID)
	}
	target := req.GetTargetPath()

	if err := os.MkdirAll(source, 0o750); err != nil {
		return nil, status.Errorf(codes.Internal, "ensure source dir: %v", err)
	}
	if err := os.MkdirAll(target, 0o750); err != nil {
		return nil, status.Errorf(codes.Internal, "ensure target dir: %v", err)
	}

	mounted, err := d.mounter.IsMountPoint(target)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "check mountpoint: %v", err)
	}
	if mounted {
		return &csi.NodePublishVolumeResponse{}, nil
	}

	if err := d.mounter.BindMount(source, target, req.GetReadonly()); err != nil {
		return nil, status.Errorf(codes.Internal, "bind mount: %v", err)
	}

	klog.Infof("NodePublishVolume: id=%s source=%s target=%s readonly=%v", volID, source, target, req.GetReadonly())
	return &csi.NodePublishVolumeResponse{}, nil
}

func (d *Driver) NodeUnpublishVolume(ctx context.Context, req *csi.NodeUnpublishVolumeRequest) (*csi.NodeUnpublishVolumeResponse, error) {
	if req.GetTargetPath() == "" {
		return nil, status.Error(codes.InvalidArgument, "missing target path")
	}
	target := req.GetTargetPath()

	mounted, err := d.mounter.IsMountPoint(target)
	if err != nil {
		if os.IsNotExist(err) {
			return &csi.NodeUnpublishVolumeResponse{}, nil
		}
		return nil, status.Errorf(codes.Internal, "check mountpoint: %v", err)
	}
	if mounted {
		if err := d.mounter.Unmount(target); err != nil && !isNotMounted(err) {
			return nil, status.Errorf(codes.Internal, "unmount: %v", err)
		}
	}

	klog.Infof("NodeUnpublishVolume: target=%s", target)
	return &csi.NodeUnpublishVolumeResponse{}, nil
}

// =========================
// Helpers
// =========================

func (d *Driver) createVolumeResponse(id, path string, size int64, pool string) *csi.CreateVolumeResponse {
	return &csi.CreateVolumeResponse{
		Volume: &csi.Volume{
			VolumeId:      id,
			CapacityBytes: size,
			VolumeContext: map[string]string{
				VolumeContextPathKey: path,
				VolumeContextPoolKey: pool,
			},
			AccessibleTopology: []*csi.Topology{
				{
					Segments: map[string]string{
						TopologyKeyNode: d.nodeID,
					},
				},
			},
		},
	}
}

func (d *Driver) checkCapacityLocked(pool string, requestedBytes int64) error {
	cfg := d.overprov.Get(pool)
	if cfg.OverprovisionRatio <= 0 {
		cfg.OverprovisionRatio = 1.0
	}

	physical, err := filesystemTotalBytes(d.volumesDir)
	if err != nil {
		return status.Errorf(codes.Internal, "statfs volumes dir: %v", err)
	}

	reserved := cfg.ReservedBytes
	if reserved < 0 {
		reserved = 0
	}
	if reserved > physical {
		reserved = physical
	}

	max := int64(float64(physical-reserved) * cfg.OverprovisionRatio)
	allocated := d.state.TotalRequestedBytes()

	if allocated+requestedBytes > max {
		return status.Errorf(codes.ResourceExhausted,
			"local capacity exhausted: requested=%d allocated=%d physical=%d reserved=%d ratio=%.2f max=%d",
			requestedBytes, allocated, physical, reserved, cfg.OverprovisionRatio, max)
	}
	return nil
}

func filesystemTotalBytes(path string) (int64, error) {
	var st syscall.Statfs_t
	if err := syscall.Statfs(path, &st); err != nil {
		return 0, err
	}
	// Blocks * Bsize is "total data blocks in filesystem" on Linux/darwin.
	return int64(st.Blocks) * int64(st.Bsize), nil
}

func sanitizeID(id string) string {
	orig := strings.TrimSpace(id)
	id = strings.Trim(orig, "/")
	if id == "" {
		return "vol-" + shortHash(orig)
	}

	var b strings.Builder
	b.Grow(len(id))
	for _, r := range id {
		switch {
		case r >= 'a' && r <= 'z':
			b.WriteRune(r)
		case r >= 'A' && r <= 'Z':
			b.WriteRune(r)
		case r >= '0' && r <= '9':
			b.WriteRune(r)
		case r == '-' || r == '_' || r == '.':
			b.WriteRune(r)
		default:
			b.WriteByte('-')
		}
	}
	out := b.String()
	out = strings.Trim(out, "-.")
	if out == "" {
		return "vol-" + shortHash(orig)
	}
	if len(out) > 128 {
		out = out[:128]
	}
	return out
}

func shortHash(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:4])
}

func topologyAllowsNode(req *csi.TopologyRequirement, nodeID string) bool {
	if req == nil {
		return true
	}
	for _, t := range req.GetRequisite() {
		if t == nil {
			continue
		}
		if t.Segments[TopologyKeyNode] == nodeID {
			return true
		}
	}
	for _, t := range req.GetPreferred() {
		if t == nil {
			continue
		}
		if t.Segments[TopologyKeyNode] == nodeID {
			return true
		}
	}
	// If there's a requirement but it doesn't explicitly mention the node key, accept it.
	for _, t := range req.GetRequisite() {
		if t == nil {
			continue
		}
		if _, ok := t.Segments[TopologyKeyNode]; !ok {
			return true
		}
	}
	return false
}

func isNotMounted(err error) bool {
	if err == nil {
		return false
	}
	// Best-effort: different kernels/runtime return different errno strings.
	s := strings.ToLower(err.Error())
	return strings.Contains(s, "not mounted") || strings.Contains(s, "invalid argument")
}
