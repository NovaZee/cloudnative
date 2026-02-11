package overprovision

import (
	"sync/atomic"

	"k8s.io/apimachinery/pkg/api/resource"
)

// PoolConfig is the on-wire configuration parsed from ConfigMap YAML.
type PoolConfig struct {
	OverprovisionRatio float64           `json:"overprovisionRatio" yaml:"overprovisionRatio"`
	ReservedBytes      resource.Quantity `json:"reservedBytes" yaml:"reservedBytes"`
}

// Config is the on-wire configuration parsed from ConfigMap YAML.
type Config struct {
	DefaultOverprovisionRatio float64                `json:"defaultOverprovisionRatio" yaml:"defaultOverprovisionRatio"`
	DefaultReservedBytes      resource.Quantity      `json:"defaultReservedBytes" yaml:"defaultReservedBytes"`
	Pools                     map[string]*PoolConfig `json:"pools" yaml:"pools"`
}

// ResolvedPoolConfig is the runtime configuration used by the CSI driver.
type ResolvedPoolConfig struct {
	OverprovisionRatio float64
	ReservedBytes      int64
}

type Provider interface {
	// Get returns the pool configuration (pool-specific if present, otherwise defaults).
	Get(pool string) ResolvedPoolConfig
}

type StaticProvider struct {
	cfg atomic.Value // Config
}

func NewStaticProvider(cfg Config) *StaticProvider {
	p := &StaticProvider{}
	p.cfg.Store(cfg)
	return p
}

func (p *StaticProvider) Get(pool string) ResolvedPoolConfig {
	cfg, _ := p.cfg.Load().(Config)

	pc := ResolvedPoolConfig{
		OverprovisionRatio: cfg.DefaultOverprovisionRatio,
		ReservedBytes:      cfg.DefaultReservedBytes.Value(),
	}

	if cfg.Pools != nil {
		if v := cfg.Pools[pool]; v != nil {
			if v.OverprovisionRatio != 0 {
				pc.OverprovisionRatio = v.OverprovisionRatio
			}
			if !v.ReservedBytes.IsZero() {
				pc.ReservedBytes = v.ReservedBytes.Value()
			}
		}
	}

	if pc.OverprovisionRatio == 0 {
		pc.OverprovisionRatio = 1.0
	}
	return pc
}

func (p *StaticProvider) set(cfg Config) {
	p.cfg.Store(cfg)
}
