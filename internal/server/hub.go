package server

import "sync"

type Hub[T any] struct {
	mu   sync.Mutex
	next int
	subs map[int]chan T
}

func NewHub[T any]() *Hub[T] { return &Hub[T]{subs: map[int]chan T{}} }
func (h *Hub[T]) Subscribe() (int, <-chan T) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.next++
	ch := make(chan T, 8)
	h.subs[h.next] = ch
	return h.next, ch
}
func (h *Hub[T]) Unsubscribe(id int) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if ch, ok := h.subs[id]; ok {
		delete(h.subs, id)
		close(ch)
	}
}
func (h *Hub[T]) Publish(v T) {
	h.mu.Lock()
	defer h.mu.Unlock()
	for _, ch := range h.subs {
		select {
		case ch <- v:
		default:
		}
	}
}
func (h *Hub[T]) Count() int { h.mu.Lock(); defer h.mu.Unlock(); return len(h.subs) }
