package worker

import (
	"context"
	"encoding/json"
	"io"
	"maps"
	"net"
	"slices"
	"sync"
	"time"

	"github.com/onatm/boxface/internal/lifecycle"
)

const brokerPort = 1025

type broker struct {
	mu      sync.Mutex
	sets    map[string]lifecycle.Credentials
	changed chan struct{}
	wait    time.Duration
}

func newBroker(wait time.Duration) *broker {
	return &broker{sets: map[string]lifecycle.Credentials{}, changed: make(chan struct{}), wait: wait}
}

func (b *broker) set(environmentID string, creds lifecycle.Credentials) {
	copied := lifecycle.Credentials{}
	for consumer, env := range creds {
		copied[consumer] = maps.Clone(env)
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	b.sets[environmentID] = copied
	close(b.changed)
	b.changed = make(chan struct{})
}

func (b *broker) clear(environmentID string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	delete(b.sets, environmentID)
}

func (b *broker) loaded(environmentID string) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	_, ok := b.sets[environmentID]
	return ok
}

func (b *broker) await(environmentID string) (lifecycle.Credentials, bool) {
	deadline := time.NewTimer(b.wait)
	defer deadline.Stop()
	for {
		b.mu.Lock()
		creds, ok := b.sets[environmentID]
		changed := b.changed
		b.mu.Unlock()
		if ok {
			return creds, true
		}
		select {
		case <-changed:
		case <-deadline.C:
			return nil, false
		}
	}
}

type brokerRequest struct {
	Consumer string `json:"consumer"`
	List     bool   `json:"list"`
	NoWait   bool   `json:"nowait"`
}

type brokerReply struct {
	Env      map[string]string `json:"env,omitempty"`
	Commands []string          `json:"commands,omitempty"`
	Error    string            `json:"error,omitempty"`
}

func (b *broker) serve(environmentID string, conn net.Conn) {
	defer conn.Close()
	conn.SetDeadline(time.Now().Add(b.wait + 5*time.Second))
	enc := json.NewEncoder(conn)
	var req brokerRequest
	if err := json.NewDecoder(io.LimitReader(conn, 1024)).Decode(&req); err != nil || (req.Consumer == "" && !req.List) {
		enc.Encode(brokerReply{Error: "request must be JSON with a consumer or list"})
		return
	}
	var creds lifecycle.Credentials
	var ok bool
	if req.NoWait {
		b.mu.Lock()
		creds, ok = b.sets[environmentID]
		b.mu.Unlock()
	} else {
		creds, ok = b.await(environmentID)
	}
	if !ok {
		enc.Encode(brokerReply{Error: "credentials for this environment are not loaded yet; try again shortly"})
		return
	}
	if req.List {
		commands := []string{}
		for name := range creds {
			if name != "session" {
				commands = append(commands, name)
			}
		}
		slices.Sort(commands)
		enc.Encode(brokerReply{Commands: commands})
		return
	}
	env := creds[req.Consumer]
	if env == nil {
		env = map[string]string{}
	}
	enc.Encode(brokerReply{Env: env})
}

func (h *Host) SetCredentials(ctx context.Context, environmentID string, creds lifecycle.Credentials) error {
	if err := validID(environmentID); err != nil {
		return err
	}
	h.broker.set(environmentID, creds)
	return nil
}
