package projection

// orderedMap preserves key insertion order while supporting O(1) key lookup.
type orderedMap[K comparable, V any] struct {
	keys  []K
	vals  []V
	index map[K]int
}

func (m *orderedMap[K, V]) set(k K, v V) {
	if m.index == nil {
		m.index = make(map[K]int)
	}
	if idx, ok := m.index[k]; ok {
		m.vals[idx] = v
		return
	}
	m.index[k] = len(m.keys)
	m.keys = append(m.keys, k)
	m.vals = append(m.vals, v)
}

func (m *orderedMap[K, V]) get(k K) (V, bool) {
	if m.index == nil {
		var zero V
		return zero, false
	}
	idx, ok := m.index[k]
	if !ok {
		var zero V
		return zero, false
	}
	return m.vals[idx], true
}

func (m *orderedMap[K, V]) delete(k K) {
	if m.index == nil {
		return
	}
	idx, ok := m.index[k]
	if !ok {
		return
	}
	delete(m.index, k)
	last := len(m.keys) - 1
	if idx < last {
		copy(m.keys[idx:], m.keys[idx+1:])
		copy(m.vals[idx:], m.vals[idx+1:])
		for i := idx; i < last; i++ {
			m.index[m.keys[i]] = i
		}
	}
	var zeroK K
	var zeroV V
	m.keys[last] = zeroK
	m.vals[last] = zeroV
	m.keys = m.keys[:last]
	m.vals = m.vals[:last]
}

func (m *orderedMap[K, V]) clear() {
	m.keys = nil
	m.vals = nil
	clear(m.index)
}
