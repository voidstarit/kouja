package operations

import "sync"

type lockTable struct {
	mu      sync.Mutex
	entries map[string]*lockEntry
}

type lockEntry struct {
	mu   sync.Mutex
	refs int
}

func newLockTable() *lockTable {
	return &lockTable{entries: make(map[string]*lockEntry)}
}

func (table *lockTable) Lock(key string) func() {
	table.mu.Lock()
	entry := table.entries[key]
	if entry == nil {
		entry = &lockEntry{}
		table.entries[key] = entry
	}
	entry.refs++
	table.mu.Unlock()

	entry.mu.Lock()
	return func() {
		entry.mu.Unlock()
		table.mu.Lock()
		entry.refs--
		if entry.refs == 0 {
			delete(table.entries, key)
		}
		table.mu.Unlock()
	}
}
