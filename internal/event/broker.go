// Package event provides a tiny in-process publish/subscribe broker. The
// scheduler publishes probe results and state changes; the HTTP layer forwards
// them to browsers over Server-Sent Events so the dashboard updates live
// without polling.
package event

import (
	"sync"
	"time"

	"github.com/j000k3r/weatherglass/internal/model"
)

// Kind classifies an event so clients can filter cheaply.
type Kind string

const (
	// KindResult is emitted after every probe.
	KindResult Kind = "result"
	// KindStatus is emitted when a sensor changes status (up <-> down).
	KindStatus Kind = "status"
	// KindIncident is emitted when an incident is opened or closed.
	KindIncident Kind = "incident"
	// KindSensor is emitted when a sensor's configuration changes.
	KindSensor Kind = "sensor"
	// KindChannel is emitted when an alert channel changes.
	KindChannel Kind = "channel"
	// KindSettings is emitted when shared UI settings change.
	KindSettings Kind = "settings"
)

// Event is the payload broadcast to subscribers.
type Event struct {
	Kind     Kind            `json:"kind"`
	SensorID int64           `json:"sensor_id,omitempty"`
	Status   model.Status    `json:"status,omitempty"`
	Result   *model.Result   `json:"result,omitempty"`
	Incident *model.Incident `json:"incident,omitempty"`
	TS       time.Time       `json:"ts"`
}

// Broker fans events out to any number of subscribers.
//
// Publishing never blocks: a subscriber whose buffer is full drops the event
// rather than stalling the scheduler. The dashboard is eventually consistent
// anyway, because every client also refetches on connect.
type Broker struct {
	mu     sync.RWMutex
	subs   map[int]chan Event
	nextID int
	closed bool
}

// NewBroker returns an empty broker.
func NewBroker() *Broker {
	return &Broker{subs: make(map[int]chan Event)}
}

// Subscribe registers a new subscriber. The returned cancel function must be
// called to release the subscription.
func (b *Broker) Subscribe() (<-chan Event, func()) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.closed {
		// Return an already-closed channel so callers terminate immediately.
		ch := make(chan Event)
		close(ch)
		return ch, func() {}
	}
	id := b.nextID
	b.nextID++
	ch := make(chan Event, 64)
	b.subs[id] = ch

	var once sync.Once
	cancel := func() {
		once.Do(func() {
			b.mu.Lock()
			defer b.mu.Unlock()
			if c, ok := b.subs[id]; ok {
				delete(b.subs, id)
				close(c)
			}
		})
	}
	return ch, cancel
}

// Publish delivers e to every subscriber, dropping it for anyone who is not
// keeping up.
func (b *Broker) Publish(e Event) {
	if e.TS.IsZero() {
		e.TS = time.Now().UTC()
	}
	b.mu.RLock()
	defer b.mu.RUnlock()
	for _, ch := range b.subs {
		select {
		case ch <- e:
		default:
		}
	}
}

// Subscribers reports how many clients are currently connected.
func (b *Broker) Subscribers() int {
	b.mu.RLock()
	defer b.mu.RUnlock()
	return len(b.subs)
}

// Close shuts the broker down and releases all subscribers.
func (b *Broker) Close() {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.closed {
		return
	}
	b.closed = true
	for id, ch := range b.subs {
		delete(b.subs, id)
		close(ch)
	}
}
