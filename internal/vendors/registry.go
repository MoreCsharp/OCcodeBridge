package vendors

import (
	"sort"
	"sync"
)

// Vendors register themselves from their package's init, so the registry never
// imports a concrete vendor and vendor packages stay free of registry knowledge.
var (
	registryMu sync.RWMutex
	registry   = map[string]Vendor{}
)

// Register adds one vendor implementation. Registering the same id twice panics:
// it can only be a duplicated id, not a runtime condition.
func Register(v Vendor) {
	if v == nil || v.ID() == "" {
		panic("vendor: Register requires a vendor with an id")
	}
	registryMu.Lock()
	defer registryMu.Unlock()
	if _, exists := registry[v.ID()]; exists {
		panic("vendor: duplicate vendor id " + v.ID())
	}
	registry[v.ID()] = v
}

// Get resolves a vendor by id.
func Get(id string) (Vendor, bool) {
	registryMu.RLock()
	defer registryMu.RUnlock()
	v, ok := registry[id]
	return v, ok
}

// IDs lists the registered vendor ids in a stable order.
func IDs() []string {
	registryMu.RLock()
	defer registryMu.RUnlock()
	ids := make([]string, 0, len(registry))
	for id := range registry {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids
}
