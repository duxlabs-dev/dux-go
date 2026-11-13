package ipc

import "sync"

// PendingTopic is the topic PendingOpen publishes on.
const PendingTopic = "ui"

// PendingOpen holds "ui" actions that arrived before the UI subscribed, so a cold start still honors them.
type PendingOpen struct {
	mu       sync.Mutex
	payloads []map[string]any
}

// Deliver publishes payload now when the UI listens and nothing is queued ahead of it, otherwise queues it.
func (p *PendingOpen) Deliver(bus *EventBus, payload map[string]any) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if len(p.payloads) == 0 && bus.HasSubscriber(PendingTopic) {
		bus.Publish(PendingTopic, payload)
		return
	}
	p.payloads = append(p.payloads, payload)
}

// Take empties the queue and returns what it held, for a caller that answers with it instead of publishing.
func (p *PendingOpen) Take() []map[string]any {
	p.mu.Lock()
	defer p.mu.Unlock()
	payloads := p.payloads
	p.payloads = nil
	return payloads
}

// Flush publishes everything queued, in order.
func (p *PendingOpen) Flush(bus *EventBus) {
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, payload := range p.payloads {
		bus.Publish(PendingTopic, payload)
	}
	p.payloads = nil
}
