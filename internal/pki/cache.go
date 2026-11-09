package pki

import "sync"

type revocationCache struct {
	mu      sync.RWMutex
	serials map[string]struct{}
}

func newRevocationCache() *revocationCache {
	return &revocationCache{serials: make(map[string]struct{})}
}

func (c *revocationCache) add(serial string) {
	c.mu.Lock()
	c.serials[serial] = struct{}{}
	c.mu.Unlock()
}

func (c *revocationCache) remove(serial string) {
	c.mu.Lock()
	delete(c.serials, serial)
	c.mu.Unlock()
}

func (c *revocationCache) has(serial string) bool {
	c.mu.RLock()
	_, ok := c.serials[serial]
	c.mu.RUnlock()
	return ok
}

func (c *revocationCache) load(serials []string) {
	c.mu.Lock()
	for _, serial := range serials {
		c.serials[serial] = struct{}{}
	}
	c.mu.Unlock()
}
