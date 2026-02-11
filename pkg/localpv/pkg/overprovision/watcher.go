package overprovision

import (
	"context"
	"fmt"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/fields"
	"k8s.io/client-go/informers"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/cache"
	"k8s.io/klog/v2"
	"sigs.k8s.io/yaml"
)

type WatcherOptions struct {
	Namespace string
	Name      string
	DataKey   string
	Resync    time.Duration
}

type Watcher struct {
	*StaticProvider

	client    kubernetes.Interface
	namespace string
	name      string
	dataKey   string
	resync    time.Duration
}

func NewWatcher(client kubernetes.Interface, opts WatcherOptions) (*Watcher, error) {
	if client == nil {
		return nil, fmt.Errorf("client is required")
	}
	if opts.Namespace == "" || opts.Name == "" {
		return nil, fmt.Errorf("namespace and name are required")
	}
	if opts.DataKey == "" {
		opts.DataKey = "config.yaml"
	}
	if opts.Resync == 0 {
		opts.Resync = 30 * time.Second
	}

	w := &Watcher{
		StaticProvider: NewStaticProvider(Config{DefaultOverprovisionRatio: 1.0}),
		client:         client,
		namespace:      opts.Namespace,
		name:           opts.Name,
		dataKey:        opts.DataKey,
		resync:         opts.Resync,
	}
	return w, nil
}

func (w *Watcher) Run(ctx context.Context) {
	// Load once (best-effort) so the initial state is correct even before informers sync.
	if err := w.loadOnce(ctx); err != nil {
		klog.Warningf("overprovision: initial load failed: %v", err)
	}

	factory := informers.NewSharedInformerFactoryWithOptions(
		w.client,
		w.resync,
		informers.WithNamespace(w.namespace),
		informers.WithTweakListOptions(func(lo *metav1.ListOptions) {
			lo.FieldSelector = fields.OneTermEqualSelector("metadata.name", w.name).String()
		}),
	)

	inf := factory.Core().V1().ConfigMaps().Informer()
	inf.AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc:    func(obj interface{}) { w.onConfigMap(obj) },
		UpdateFunc: func(_, obj interface{}) { w.onConfigMap(obj) },
		DeleteFunc: func(obj interface{}) {
			klog.Warningf("overprovision: configmap %s/%s deleted, reverting to defaults", w.namespace, w.name)
			w.set(Config{DefaultOverprovisionRatio: 1.0})
		},
	})

	factory.Start(ctx.Done())
	if !cache.WaitForCacheSync(ctx.Done(), inf.HasSynced) {
		klog.Warningf("overprovision: informer cache sync failed")
		return
	}

	<-ctx.Done()
}

func (w *Watcher) loadOnce(ctx context.Context) error {
	cm, err := w.client.CoreV1().ConfigMaps(w.namespace).Get(ctx, w.name, metav1.GetOptions{})
	if err != nil {
		return err
	}
	return w.applyConfigMap(cm)
}

func (w *Watcher) onConfigMap(obj interface{}) {
	cm, ok := obj.(*corev1.ConfigMap)
	if !ok || cm == nil {
		return
	}
	if cm.Name != w.name || cm.Namespace != w.namespace {
		return
	}
	if err := w.applyConfigMap(cm); err != nil {
		klog.Warningf("overprovision: apply configmap %s/%s failed: %v", w.namespace, w.name, err)
	}
}

func (w *Watcher) applyConfigMap(cm *corev1.ConfigMap) error {
	raw := ""
	if cm.Data != nil {
		raw = cm.Data[w.dataKey]
	}
	if raw == "" {
		return fmt.Errorf("missing data key %q", w.dataKey)
	}
	var cfg Config
	if err := yaml.Unmarshal([]byte(raw), &cfg); err != nil {
		return fmt.Errorf("parse yaml: %w", err)
	}
	if cfg.DefaultOverprovisionRatio == 0 {
		cfg.DefaultOverprovisionRatio = 1.0
	}

	w.set(cfg)
	klog.Infof("overprovision: reloaded from configmap %s/%s (defaultRatio=%.2f)", w.namespace, w.name, cfg.DefaultOverprovisionRatio)
	return nil
}
