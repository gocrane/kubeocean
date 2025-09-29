package proxier

import (
	"fmt"
	"sync"
)

// GlobalVNodePortMapper is a global mapper for VNode name to port mapping
type GlobalVNodePortMapper struct {
	mu    sync.RWMutex
	nodes map[string]string // key: vnode name, value: port
}

// VNodePortMapper is the global instance
var VNodePortMapper = &GlobalVNodePortMapper{
	nodes: make(map[string]string),
}

// AddVNodePort adds a VNode to port mapping
func (m *GlobalVNodePortMapper) AddVNodePort(vnodeName, port string) {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.nodes[vnodeName] = port
}

// RemoveVNodePort removes a VNode to port mapping
func (m *GlobalVNodePortMapper) RemoveVNodePort(vnodeName string) {
	m.mu.Lock()
	defer m.mu.Unlock()

	delete(m.nodes, vnodeName)
}

// GetPortByVNodeName gets the port for a VNode name
func (m *GlobalVNodePortMapper) GetPortByVNodeName(vnodeName string) (string, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	port, exists := m.nodes[vnodeName]
	return port, exists
}

// GetAllVNodes returns all VNode names
func (m *GlobalVNodePortMapper) GetAllVNodes() []string {
	m.mu.RLock()
	defer m.mu.RUnlock()

	var vnodes []string
	for vnodeName := range m.nodes {
		vnodes = append(vnodes, vnodeName)
	}
	return vnodes
}

// GetVNodeCount returns the number of VNodes
func (m *GlobalVNodePortMapper) GetVNodeCount() int {
	m.mu.RLock()
	defer m.mu.RUnlock()

	return len(m.nodes)
}

// Clear removes all mappings
func (m *GlobalVNodePortMapper) Clear() {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.nodes = make(map[string]string)
}

// String returns a string representation of the mapper
func (m *GlobalVNodePortMapper) String() string {
	m.mu.RLock()
	defer m.mu.RUnlock()

	return fmt.Sprintf("GlobalVNodePortMapper{count: %d, nodes: %v}", len(m.nodes), m.nodes)
}
