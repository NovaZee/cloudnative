package state

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
)

type Volume struct {
	ID             string `json:"id"`
	RequestedBytes int64  `json:"requestedBytes"`
	Pool           string `json:"pool,omitempty"`
}

type Store interface {
	Load() error
	Get(id string) (Volume, bool)
	Upsert(v Volume) error
	Delete(id string) error
	TotalRequestedBytes() int64
}

type FileStore struct {
	path string

	mu      sync.Mutex
	volumes map[string]Volume
}

func NewFileStore(path string) (*FileStore, error) {
	if path == "" {
		return nil, fmt.Errorf("path is required")
	}
	return &FileStore{
		path:    path,
		volumes: make(map[string]Volume),
	}, nil
}

func (s *FileStore) Load() error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if err := os.MkdirAll(filepath.Dir(s.path), 0o750); err != nil {
		return err
	}

	b, err := os.ReadFile(s.path)
	if err != nil {
		if os.IsNotExist(err) {
			return s.saveLocked()
		}
		return err
	}
	if len(b) == 0 {
		s.volumes = make(map[string]Volume)
		return nil
	}

	var vols []Volume
	if err := json.Unmarshal(b, &vols); err != nil {
		return fmt.Errorf("unmarshal %s: %w", s.path, err)
	}
	s.volumes = make(map[string]Volume, len(vols))
	for _, v := range vols {
		if v.ID == "" {
			continue
		}
		s.volumes[v.ID] = v
	}
	return nil
}

func (s *FileStore) Get(id string) (Volume, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	v, ok := s.volumes[id]
	return v, ok
}

func (s *FileStore) Upsert(v Volume) error {
	if v.ID == "" {
		return fmt.Errorf("volume ID is required")
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	s.volumes[v.ID] = v
	return s.saveLocked()
}

func (s *FileStore) Delete(id string) error {
	if id == "" {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.volumes, id)
	return s.saveLocked()
}

func (s *FileStore) TotalRequestedBytes() int64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	var total int64
	for _, v := range s.volumes {
		total += v.RequestedBytes
	}
	return total
}

func (s *FileStore) saveLocked() error {
	vols := make([]Volume, 0, len(s.volumes))
	for _, v := range s.volumes {
		vols = append(vols, v)
	}

	b, err := json.MarshalIndent(vols, "", "  ")
	if err != nil {
		return err
	}

	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o640); err != nil {
		return err
	}
	return os.Rename(tmp, s.path)
}
