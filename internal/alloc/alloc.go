package alloc

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

type Allocator struct {
	basePort int
	m        map[string]int
	used     map[int]bool
}

func New(basePort int) *Allocator {
	return &Allocator{
		basePort: basePort,
		m:        make(map[string]int),
		used:     make(map[int]bool),
	}
}

func (a *Allocator) Port(key string) (int, error) {
	if p, ok := a.m[key]; ok {
		return p, nil
	}
	return a.newPort(key)
}

// PortFor returns the port for a stable node key, claiming the previous
// exit-IP mapping when this is the first run after the keyed-state migration.
func (a *Allocator) PortFor(key, legacyKey string) (int, error) {
	if p, ok := a.m[key]; ok {
		return p, nil
	}
	if legacyKey != "" {
		if p, ok := a.m[legacyKey]; ok {
			delete(a.m, legacyKey)
			a.m[key] = p
			a.used[p] = true
			return p, nil
		}
	}
	return a.newPort(key)
}

func (a *Allocator) newPort(key string) (int, error) {
	p := a.basePort
	for a.used[p] {
		p++
		if p > 65535 {
			return 0, fmt.Errorf("port range exhausted")
		}
	}
	a.m[key] = p
	a.used[p] = true
	return p, nil
}

func (a *Allocator) Load(path string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	var m map[string]int
	if err := json.Unmarshal(data, &m); err != nil {
		return fmt.Errorf("parse state file: %w", err)
	}
	a.m = m
	a.used = make(map[int]bool)
	for _, p := range m {
		a.used[p] = true
	}
	return nil
}

func (a *Allocator) Save(path string) error {
	data, err := json.MarshalIndent(a.m, "", "  ")
	if err != nil {
		return err
	}
	dir := filepath.Dir(path)
	if dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0755); err != nil {
			return err
		}
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// RetainPort keeps an already-bound listener port stable under its node key.
func (a *Allocator) RetainPort(key string, port int) {
	for oldKey, p := range a.m {
		if p == port && oldKey != key {
			delete(a.m, oldKey)
		}
	}
	a.m[key] = port
	a.used[port] = true
}
