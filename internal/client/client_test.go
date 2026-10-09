package client

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"sync"
	"testing"
	"time"
)

func TestFollowReconnectsFromLastEvent(t *testing.T) {
	var mu sync.Mutex
	var lastIDs []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		lastIDs = append(lastIDs, r.Header.Get("Last-Event-ID"))
		first := len(lastIDs) == 1
		mu.Unlock()
		w.Header().Set("Content-Type", "text/event-stream")
		if first {
			fmt.Fprint(w, "id: 1\nevent: step\ndata: {\"step\":\"create_disk\"}\n\nid: 2\nevent: step\ndata: {\"step\":\"create_network\"}\n\n")
			return
		}
		fmt.Fprint(w, "id: 3\nevent: step\ndata: {\"step\":\"start_vm\"}\n\nid: 4\nevent: state\ndata: {\"id\":\"op\",\"state\":\"succeeded\"}\n\n")
	}))
	defer srv.Close()

	c := New(srv.URL, "token")
	c.RetryDelay = time.Millisecond
	var steps []string
	op, err := c.Follow(context.Background(), "op", func(s string) { steps = append(steps, s) })
	if err != nil || op.State != "succeeded" {
		t.Fatalf("op = %+v, %v", op, err)
	}
	if want := []string{"create_disk", "create_network", "start_vm"}; !slices.Equal(steps, want) {
		t.Fatalf("steps = %v, want %v", steps, want)
	}
	if want := []string{"", "2"}; !slices.Equal(lastIDs, want) {
		t.Fatalf("Last-Event-ID headers = %q, want %q", lastIDs, want)
	}
}

func TestAPIErrorsAreStructured(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusConflict)
		fmt.Fprint(w, `{"error":{"code":"capacity_exceeded","message":"environments would be 5, limit 4","retryable":false}}`)
	}))
	defer srv.Close()
	_, err := New(srv.URL, "token").Environments(context.Background())
	e, ok := err.(*Error)
	if !ok || e.Status != http.StatusConflict || e.Code != "capacity_exceeded" {
		t.Fatalf("err = %#v", err)
	}
}

func TestUnreachableServerRetriesThenFails(t *testing.T) {
	c := New("http://127.0.0.1:1", "token")
	c.RetryDelay = time.Millisecond
	if _, err := c.Environments(context.Background()); err == nil {
		t.Fatal("expected an error")
	}
}
