package sshcredential

import "sync"

// Memory belongs to one operation. Nothing here is serializable or persisted.
type Memory struct {
	mu     sync.Mutex
	values map[string][]byte
}

func (m *Memory) Set(c Context, secret []byte) error {
	if c.Validate() != nil || validateSecret(secret) != nil {
		return ErrUnsafe
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.values == nil {
		m.values = map[string][]byte{}
	}
	Wipe(m.values[c.ID()])
	m.values[c.ID()] = append([]byte(nil), secret...)
	return nil
}
func (m *Memory) Get(c Context) ([]byte, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	v, ok := m.values[c.ID()]
	return append([]byte(nil), v...), ok
}
func (m *Memory) Close() {
	m.mu.Lock()
	defer m.mu.Unlock()
	for k, v := range m.values {
		Wipe(v)
		delete(m.values, k)
	}
}
