package agent

import (
	"context"
	"errors"
	"fmt"
	"sync"

	"github.com/voidstarit/kouja/internal/config"
)

var ErrClassConflict = config.ErrClassConflict

// ClassRegistry is the live storage-class map used by the volume and admin
// services. Writes persist through the configuration file and then swap the
// in-memory map under one lock.
type ClassRegistry interface {
	Snapshot() map[string]string
	ClassName(vg string) string
	Resolve(class, vg string) (resolvedClass, resolvedVG string, err error)
	Set(ctx context.Context, class, vg string) (warnings []string, err error)
	Remove(ctx context.Context, class string) (warnings []string, err error)
}

type ClassMap struct {
	mu         sync.RWMutex
	classes    map[string]string
	configPath string
}

var _ ClassRegistry = (*ClassMap)(nil)

func NewClassMap(classes map[string]string, configPath string) *ClassMap {
	copyOfClasses := make(map[string]string, len(classes))
	for class, vg := range classes {
		copyOfClasses[class] = vg
	}
	return &ClassMap{classes: copyOfClasses, configPath: configPath}
}

func (m *ClassMap) Snapshot() map[string]string {
	m.mu.RLock()
	defer m.mu.RUnlock()
	result := make(map[string]string, len(m.classes))
	for class, vg := range m.classes {
		result[class] = vg
	}
	return result
}

func (m *ClassMap) ClassName(vg string) string {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return config.StorageClassForVG(m.classes, vg)
}

func (m *ClassMap) Resolve(class, vg string) (string, string, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return resolveStorageClass(m.classes, class, vg)
}

func (m *ClassMap) Set(_ context.Context, class, vg string) ([]string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.configPath == "" {
		return nil, fmt.Errorf("agent: class registry has no configuration path")
	}
	for otherClass, otherVG := range m.classes {
		if otherClass != class && otherVG == vg {
			return nil, fmt.Errorf("%w: class %q already maps to VG %q", ErrClassConflict, otherClass, vg)
		}
	}
	warnings, err := config.SetClass(m.configPath, class, vg)
	if err != nil {
		return nil, err
	}
	m.classes[class] = vg
	return warnings, nil
}

func (m *ClassMap) Remove(_ context.Context, class string) ([]string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.configPath == "" {
		return nil, fmt.Errorf("agent: class registry has no configuration path")
	}
	if _, ok := m.classes[class]; !ok {
		return nil, config.ErrClassNotFound
	}
	warnings, err := config.RemoveClass(m.configPath, class)
	if err != nil {
		return nil, err
	}
	delete(m.classes, class)
	return warnings, nil
}

// IsClassNotFound reports whether err is the missing-class sentinel.
func IsClassNotFound(err error) bool {
	return errors.Is(err, config.ErrClassNotFound)
}
