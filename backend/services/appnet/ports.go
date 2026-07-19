package appnet

import "sync"

// PortPool manages a reusable pool of host ports for app namespaces.
// When an app closes, its port goes back into the pool.
type PortPool struct {
	mu        sync.Mutex
	min       int
	max       int
	allocated map[int]string // port -> appID
	freed     []int          // recycled ports, LIFO
}

func NewPortPool(min, max int) *PortPool {
	return &PortPool{
		min:       min,
		max:       max,
		allocated: make(map[int]string),
	}
}

// Allocate assigns a port to an app. Reuses freed ports first.
func (p *PortPool) Allocate(appID string) (int, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()

	// Check if app already has a port
	for port, id := range p.allocated {
		if id == appID {
			return port, true
		}
	}

	// Reuse a freed port
	if len(p.freed) > 0 {
		port := p.freed[len(p.freed)-1]
		p.freed = p.freed[:len(p.freed)-1]
		p.allocated[port] = appID
		return port, true
	}

	// Find next available
	for port := p.min; port <= p.max; port++ {
		if _, taken := p.allocated[port]; !taken {
			p.allocated[port] = appID
			return port, true
		}
	}

	return 0, false // pool exhausted
}

// Release returns a port to the pool for reuse.
func (p *PortPool) Release(appID string) int {
	p.mu.Lock()
	defer p.mu.Unlock()

	for port, id := range p.allocated {
		if id == appID {
			delete(p.allocated, port)
			p.freed = append(p.freed, port)
			return port
		}
	}
	return 0
}

// InUse returns how many ports are currently allocated.
func (p *PortPool) InUse() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.allocated)
}

// Available returns how many ports are available (freed + unallocated).
func (p *PortPool) Available() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	total := p.max - p.min + 1
	return total - len(p.allocated)
}
