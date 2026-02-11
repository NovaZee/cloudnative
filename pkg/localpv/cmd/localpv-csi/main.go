package main

import (
	"context"
	"flag"
	"os"
	"os/signal"
	"syscall"
	"time"

	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	"k8s.io/klog/v2"

	"cloudnative/pkg/localpv/pkg/driver"
	"cloudnative/pkg/localpv/pkg/overprovision"
	"cloudnative/pkg/localpv/pkg/server"
	"cloudnative/pkg/localpv/pkg/state"
)

func main() {
	klog.InitFlags(nil)

	endpoint := flag.String("endpoint", getenv("CSI_ENDPOINT", "unix:///csi/csi.sock"), "CSI endpoint (e.g. unix:///csi/csi.sock)")
	driverName := flag.String("driver-name", getenv("LOCALPV_DRIVER_NAME", driver.DefaultDriverName), "CSI driver name")
	nodeID := flag.String("node-id", getenv("NODE_ID", ""), "Node ID (defaults to spec.nodeName/hostname)")
	baseDir := flag.String("base-dir", getenv("LOCALPV_BASE_DIR", "/var/lib/localpv"), "Host path base directory for local volumes")
	volumesDir := flag.String("volumes-dir", getenv("LOCALPV_VOLUMES_DIR", ""), "Override volumes directory (default: <base-dir>/volumes)")
	stateFile := flag.String("state-file", getenv("LOCALPV_STATE_FILE", ""), "Override state file path (default: <base-dir>/.localpv-state.json)")

	configMapNamespace := flag.String("configmap-namespace", getenv("LOCALPV_CONFIGMAP_NAMESPACE", "localpv-system"), "Namespace of the overprovision ConfigMap")
	configMapName := flag.String("configmap-name", getenv("LOCALPV_CONFIGMAP_NAME", "localpv-overprovision"), "Name of the overprovision ConfigMap")
	configMapKey := flag.String("configmap-key", getenv("LOCALPV_CONFIGMAP_KEY", "config.yaml"), "ConfigMap data key that stores YAML config")
	kubeconfig := flag.String("kubeconfig", getenv("KUBECONFIG", ""), "Path to a kubeconfig (optional; in-cluster config used if empty)")

	flag.Parse()

	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	if *nodeID == "" {
		host, err := os.Hostname()
		if err != nil {
			klog.Fatalf("get hostname: %v", err)
		}
		*nodeID = host
	}

	if *volumesDir == "" {
		*volumesDir = driver.DefaultVolumesDir(*baseDir)
	}
	if *stateFile == "" {
		*stateFile = driver.DefaultStateFile(*baseDir)
	}

	st, err := state.NewFileStore(*stateFile)
	if err != nil {
		klog.Fatalf("init state store: %v", err)
	}
	if err := st.Load(); err != nil {
		klog.Fatalf("load state store: %v", err)
	}

	var kubeClient kubernetes.Interface
	if *kubeconfig != "" {
		cfg, err := clientcmd.BuildConfigFromFlags("", *kubeconfig)
		if err != nil {
			klog.Fatalf("build kubeconfig: %v", err)
		}
		kubeClient, err = kubernetes.NewForConfig(cfg)
		if err != nil {
			klog.Fatalf("create kube client: %v", err)
		}
	} else {
		cfg, err := rest.InClusterConfig()
		if err != nil {
			klog.Warningf("in-cluster config not available, overprovision ConfigMap watch disabled: %v", err)
		} else {
			kubeClient, err = kubernetes.NewForConfig(cfg)
			if err != nil {
				klog.Warningf("create kube client failed, overprovision ConfigMap watch disabled: %v", err)
			}
		}
	}

	var overprov overprovision.Provider = overprovision.NewStaticProvider(overprovision.Config{
		DefaultOverprovisionRatio: 1.0,
	})
	if kubeClient != nil {
		w, err := overprovision.NewWatcher(kubeClient, overprovision.WatcherOptions{
			Namespace: *configMapNamespace,
			Name:      *configMapName,
			DataKey:   *configMapKey,
			Resync:    30 * time.Second,
		})
		if err != nil {
			klog.Warningf("init overprovision watcher failed, using default ratio=1.0: %v", err)
		} else {
			overprov = w
			go w.Run(ctx)
		}
	}

	d, err := driver.New(driver.Options{
		Name:       *driverName,
		Version:    driver.DefaultVersion,
		NodeID:     *nodeID,
		BaseDir:    *baseDir,
		VolumesDir: *volumesDir,
		State:      st,
		Overprov:   overprov,
	})
	if err != nil {
		klog.Fatalf("init driver: %v", err)
	}

	s, err := server.New(server.Options{
		Endpoint: *endpoint,
	})
	if err != nil {
		klog.Fatalf("init server: %v", err)
	}

	if err := s.Start(d); err != nil {
		klog.Fatalf("start server: %v", err)
	}
	defer s.Stop()

	<-ctx.Done()
	klog.Flush()
}

func getenv(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}
