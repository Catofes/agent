package server

import (
	"testing"
	"time"
)

func TestHubSlowSubscriberDoesNotBlockPublisher(t *testing.T) {
	hub := NewHub[int]()
	id, ch := hub.Subscribe()
	defer hub.Unsubscribe(id)

	done := make(chan struct{})
	go func() {
		for i := 0; i < 10_000; i++ {
			hub.Publish(i)
		}
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("slow subscriber blocked event publication")
	}

	if got := len(ch); got != cap(ch) {
		t.Fatalf("buffered events=%d want=%d", got, cap(ch))
	}
}

func TestHubUnsubscribeReleasesSubscriber(t *testing.T) {
	hub := NewHub[string]()
	id, ch := hub.Subscribe()
	if hub.Count() != 1 {
		t.Fatalf("subscribers=%d", hub.Count())
	}
	hub.Unsubscribe(id)
	if hub.Count() != 0 {
		t.Fatalf("subscribers after unsubscribe=%d", hub.Count())
	}
	if _, open := <-ch; open {
		t.Fatal("subscriber channel remained open")
	}
}
