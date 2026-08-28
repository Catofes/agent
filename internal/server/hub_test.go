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

func TestTargetHubOnlyPublishesToMatchingSubscribers(t *testing.T) {
	hub := NewTargetHub[string]()
	firstID, first := hub.Subscribe("run-one\x00student")
	defer hub.Unsubscribe(firstID)
	secondID, second := hub.Subscribe("run-two\x00student")
	defer hub.Unsubscribe(secondID)

	hub.Publish("run-one\x00student", "memory changed")
	select {
	case got := <-first:
		if got != "memory changed" {
			t.Fatalf("message=%q", got)
		}
	case <-time.After(time.Second):
		t.Fatal("matching subscriber did not receive message")
	}
	select {
	case got := <-second:
		t.Fatalf("non-matching subscriber received %q", got)
	default:
	}
}
