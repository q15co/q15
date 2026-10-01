package server

import (
	"sync"

	"github.com/q15co/q15/systems/web/internal/protocol"
)

const maxDevicesPerPrincipal = 32

type connection interface {
	enqueue(protocol.Frame) bool
	stop()
}

// registry performs bounded, nonblocking fan-out. A slow device is disconnected
// and can recover from the agent's transcript without stalling another device.
type registry struct {
	mu      sync.Mutex
	devices map[string]map[connection]struct{}
}

func newRegistry() *registry { return &registry{devices: make(map[string]map[connection]struct{})} }

func (r *registry) add(principal string, conn connection) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	devices := r.devices[principal]
	if len(devices) >= maxDevicesPerPrincipal {
		return false
	}
	if devices == nil {
		devices = make(map[connection]struct{})
		r.devices[principal] = devices
	}
	devices[conn] = struct{}{}
	return true
}

func (r *registry) remove(principal string, conn connection) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.devices[principal], conn)
	if len(r.devices[principal]) == 0 {
		delete(r.devices, principal)
	}
}

func (r *registry) broadcast(principal string, frame protocol.Frame) {
	r.mu.Lock()
	connections := make([]connection, 0, len(r.devices[principal]))
	for conn := range r.devices[principal] {
		connections = append(connections, conn)
	}
	r.mu.Unlock()
	for _, conn := range connections {
		if !conn.enqueue(frame) {
			conn.stop()
			r.remove(principal, conn)
		}
	}
}

func (r *registry) close() {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, devices := range r.devices {
		for conn := range devices {
			conn.stop()
		}
	}
	clear(r.devices)
}
