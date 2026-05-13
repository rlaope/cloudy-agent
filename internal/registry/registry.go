// Package registry is a generic, thread-safe, name-keyed store of T. It is
// the shared substrate behind llm.providers, tools.Registry, and
// skills.Registry — three previously-divergent self-registration patterns
// that now share the same storage shape.
//
// nameOf is supplied by the caller so T does not need to satisfy a Named
// interface; this matters because *skills.Skill has a Name field (and so
// cannot also have a Name() method), while llm.Provider and tools.Tool both
// declare Name() on the interface.
package registry

import (
	"fmt"
	"sort"
	"sync"
)

// Map holds T values keyed by Name. The zero value is not usable; use New.
type Map[T any] struct {
	nameOf func(T) string
	mu     sync.RWMutex
	items  map[string]T
}

// New returns an empty Map. nameOf must be non-nil and stable across calls;
// the function is only invoked while inserting.
func New[T any](nameOf func(T) string) *Map[T] {
	if nameOf == nil {
		panic("registry: nameOf must not be nil")
	}
	return &Map[T]{nameOf: nameOf, items: map[string]T{}}
}

// Register adds it. Returns an error when a value with the same Name is
// already registered.
func (m *Map[T]) Register(it T) error {
	name := m.nameOf(it)
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, exists := m.items[name]; exists {
		return fmt.Errorf("registry: %q already registered", name)
	}
	m.items[name] = it
	return nil
}

// MustRegister adds it, panicking on duplicate.
func (m *Map[T]) MustRegister(it T) {
	if err := m.Register(it); err != nil {
		panic(err.Error())
	}
}

// Replace inserts or overwrites the value at it's Name. Use this when the
// caller knows duplicates should win (e.g. user-skills overriding builtins).
func (m *Map[T]) Replace(it T) {
	name := m.nameOf(it)
	m.mu.Lock()
	m.items[name] = it
	m.mu.Unlock()
}

// Get returns the value registered under name and whether it was found.
func (m *Map[T]) Get(name string) (T, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	it, ok := m.items[name]
	return it, ok
}

// All returns every registered value, sorted alphabetically by Name.
func (m *Map[T]) All() []T {
	m.mu.RLock()
	keys := make([]string, 0, len(m.items))
	for k := range m.items {
		keys = append(keys, k)
	}
	m.mu.RUnlock()
	sort.Strings(keys)
	out := make([]T, 0, len(keys))
	for _, k := range keys {
		if v, ok := m.Get(k); ok {
			out = append(out, v)
		}
	}
	return out
}

// Len returns the number of registered values.
func (m *Map[T]) Len() int {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return len(m.items)
}
