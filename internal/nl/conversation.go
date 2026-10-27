package nl

import (
	"crypto/rand"
	"encoding/hex"
	"slices"
	"sync"
	"time"

	"github.com/dbscope/dbscope/internal/nl/llm"
	"github.com/dbscope/dbscope/internal/nl/retrieve"
)

// Conversation limits (docs/API.md "Ask").
const (
	MaxTurns         = 6
	ConversationTTL  = 30 * time.Minute
	MaxConversations = 200
)

// turn is one answered question as the model saw it.
type turn struct {
	user   string // the full user message
	output string // the model's JSON output
	tables []string
}

// pending is an answer waiting for the user to confirm cost or rows; a
// re-send of the same question with a confirm flag runs it unchanged
// instead of asking the model again.
type pending struct {
	question string
	user     string
	output   llm.Output
	raw      string
	repaired bool
	context  retrieve.Result
}

type conversation struct {
	profileID string
	turns     []turn
	pending   *pending
	lastUsed  time.Time
}

// Conversations keeps recent turns in memory, per conversation ID. Entries
// expire after ConversationTTL idle; when MaxConversations are held the
// least recently used is dropped. Safe for concurrent use.
type Conversations struct {
	mu   sync.Mutex
	byID map[string]*conversation
	now  func() time.Time
	ttl  time.Duration
	max  int
}

func NewConversations() *Conversations {
	return &Conversations{byID: map[string]*conversation{}, now: time.Now, ttl: ConversationTTL, max: MaxConversations}
}

func newID() string {
	b := make([]byte, 16)
	rand.Read(b)
	return hex.EncodeToString(b)
}

// state is a snapshot of one conversation.
type state struct {
	id      string
	turns   []turn
	pending *pending
}

// load returns the conversation's snapshot, or a fresh ID when id is
// empty, unknown, expired or belongs to another profile.
func (c *Conversations) load(profileID, id string) state {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.expire()
	if conv := c.byID[id]; id != "" && conv != nil && conv.profileID == profileID {
		conv.lastUsed = c.now()
		return state{id: id, turns: slices.Clone(conv.turns), pending: conv.pending}
	}
	return state{id: newID()}
}

// update applies f to the conversation, creating it if needed.
func (c *Conversations) update(profileID, id string, f func(*conversation)) {
	c.mu.Lock()
	defer c.mu.Unlock()
	conv := c.byID[id]
	if conv == nil || conv.profileID != profileID {
		if len(c.byID) >= c.max {
			c.evictOldest()
		}
		conv = &conversation{profileID: profileID}
		c.byID[id] = conv
	}
	conv.lastUsed = c.now()
	f(conv)
}

func (c *Conversations) addTurn(profileID, id string, t turn) {
	c.update(profileID, id, func(conv *conversation) {
		conv.turns = append(conv.turns, t)
		if n := len(conv.turns); n > MaxTurns {
			conv.turns = slices.Clone(conv.turns[n-MaxTurns:])
		}
		conv.pending = nil
	})
}

func (c *Conversations) setPending(profileID, id string, p *pending) {
	c.update(profileID, id, func(conv *conversation) { conv.pending = p })
}

// Len is the number of live conversations.
func (c *Conversations) Len() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.expire()
	return len(c.byID)
}

func (c *Conversations) expire() {
	cutoff := c.now().Add(-c.ttl)
	for id, conv := range c.byID {
		if conv.lastUsed.Before(cutoff) {
			delete(c.byID, id)
		}
	}
}

func (c *Conversations) evictOldest() {
	var oldest string
	var at time.Time
	for id, conv := range c.byID {
		if oldest == "" || conv.lastUsed.Before(at) {
			oldest, at = id, conv.lastUsed
		}
	}
	delete(c.byID, oldest)
}

// Forget drops every conversation of a profile (e.g. it was deleted).
func (c *Conversations) Forget(profileID string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for id, conv := range c.byID {
		if conv.profileID == profileID {
			delete(c.byID, id)
		}
	}
}
