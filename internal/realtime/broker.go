// Package realtime is an in-process pub/sub used to push changes to SSE clients.
// Notifications carry no payload: subscribers re-read state when poked, so a
// slow consumer can never fall behind — pending pokes coalesce into one.
package realtime

import (
	"strconv"
	"sync"
)

// Topic names.
const TopicLessons = "lessons"

// LessonTopic returns the topic for one lesson.
func LessonTopic(lessonID int64) string { return "lesson:" + strconv.FormatInt(lessonID, 10) }

// Broker fans out notifications to subscribers of a topic.
type Broker struct {
	mu     sync.Mutex
	topics map[string]map[chan struct{}]struct{}
}

// NewBroker creates an empty broker.
func NewBroker() *Broker {
	return &Broker{topics: make(map[string]map[chan struct{}]struct{})}
}

// Subscribe returns a channel poked on every Publish to topic, and a cancel
// function that must be called to release it.
func (b *Broker) Subscribe(topic string) (<-chan struct{}, func()) {
	ch := make(chan struct{}, 1)
	b.mu.Lock()
	subs := b.topics[topic]
	if subs == nil {
		subs = make(map[chan struct{}]struct{})
		b.topics[topic] = subs
	}
	subs[ch] = struct{}{}
	b.mu.Unlock()

	var once sync.Once
	return ch, func() {
		once.Do(func() {
			b.mu.Lock()
			delete(b.topics[topic], ch)
			if len(b.topics[topic]) == 0 {
				delete(b.topics, topic)
			}
			b.mu.Unlock()
		})
	}
}

// Publish pokes all subscribers of the topics without blocking.
func (b *Broker) Publish(topics ...string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	for _, t := range topics {
		for ch := range b.topics[t] {
			select {
			case ch <- struct{}{}:
			default: // a poke is already pending
			}
		}
	}
}

// Subscribers returns the number of subscribers of a topic (for tests/metrics).
func (b *Broker) Subscribers(topic string) int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return len(b.topics[topic])
}
