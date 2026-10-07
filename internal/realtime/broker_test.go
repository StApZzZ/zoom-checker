package realtime

import (
	"testing"
	"time"
)

func TestBrokerPublishCoalesces(t *testing.T) {
	t.Parallel()
	b := NewBroker()
	ch, cancel := b.Subscribe(LessonTopic(7))
	defer cancel()

	b.Publish(LessonTopic(7))
	b.Publish(LessonTopic(7)) // must not block, coalesces
	b.Publish(LessonTopic(8)) // other topic

	select {
	case <-ch:
	case <-time.After(time.Second):
		t.Fatal("expected a poke")
	}
	select {
	case <-ch:
		t.Fatal("pokes must coalesce into one")
	default:
	}
}

func TestBrokerCancel(t *testing.T) {
	t.Parallel()
	b := NewBroker()
	_, cancel := b.Subscribe(TopicLessons)
	if n := b.Subscribers(TopicLessons); n != 1 {
		t.Fatalf("subscribers = %d, want 1", n)
	}
	cancel()
	cancel() // idempotent
	if n := b.Subscribers(TopicLessons); n != 0 {
		t.Fatalf("subscribers = %d, want 0", n)
	}
	b.Publish(TopicLessons) // no subscribers: no panic
}
