package logger

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"
)

// LogEntry represents a single structured log entry sent to UI/subscribers.
type LogEntry struct {
	Timestamp string `json:"timestamp"`
	Level     string `json:"level"`
	Message   string `json:"message"`
}

// Broadcaster manages a ring buffer of recent logs and real-time subscribers.
type Broadcaster struct {
	mu          sync.RWMutex
	capacity    int
	entries     []LogEntry
	start       int
	count       int
	subscribers map[chan LogEntry]struct{}
}

// NewBroadcaster creates a new Broadcaster with given buffer capacity.
func NewBroadcaster(capacity int) *Broadcaster {
	if capacity <= 0 {
		capacity = 500
	}
	return &Broadcaster{
		capacity:    capacity,
		entries:     make([]LogEntry, capacity),
		subscribers: make(map[chan LogEntry]struct{}),
	}
}

// Success logs a message at Info level with level_tag=SUCCESS on the given logger.
func Success(logger *slog.Logger, msg string, args ...any) {
	if logger == nil {
		logger = slog.Default()
	}
	logger.Info(msg, append(args, slog.String("level_tag", "SUCCESS"))...)
}

// LogSuccess logs a message at Info level with level_tag=SUCCESS using the default slog logger.
func LogSuccess(msg string, args ...any) {
	slog.Default().Info(msg, append(args, slog.String("level_tag", "SUCCESS"))...)
}

// Add appends a new entry to the ring buffer and dispatches it to subscribers.
func (b *Broadcaster) Add(entry LogEntry) {
	b.mu.Lock()
	if b.count < b.capacity {
		b.entries[(b.start+b.count)%b.capacity] = entry
		b.count++
	} else {
		b.entries[b.start] = entry
		b.start = (b.start + 1) % b.capacity
	}

	// Copy subscribers list for fanout without holding lock during send
	subs := make([]chan LogEntry, 0, len(b.subscribers))
	for ch := range b.subscribers {
		subs = append(subs, ch)
	}
	b.mu.Unlock()

	for _, ch := range subs {
		select {
		case ch <- entry:
		default:
			// Non-blocking drop if consumer is slow
		}
	}
}

// GetHistory returns all buffered log entries in chronological order.
func (b *Broadcaster) GetHistory() []LogEntry {
	b.mu.RLock()
	defer b.mu.RUnlock()

	result := make([]LogEntry, b.count)
	for i := 0; i < b.count; i++ {
		result[i] = b.entries[(b.start+i)%b.capacity]
	}
	return result
}

// Subscribe registers a new subscriber channel. The returned cancel func removes it.
func (b *Broadcaster) Subscribe(bufSize int) (<-chan LogEntry, func()) {
	if bufSize <= 0 {
		bufSize = 100
	}
	ch := make(chan LogEntry, bufSize)

	b.mu.Lock()
	b.subscribers[ch] = struct{}{}
	b.mu.Unlock()

	cancel := func() {
		b.mu.Lock()
		if _, exists := b.subscribers[ch]; exists {
			delete(b.subscribers, ch)
			close(ch)
		}
		b.mu.Unlock()
	}

	return ch, cancel
}

// Clear removes all buffered log entries.
func (b *Broadcaster) Clear() {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.start = 0
	b.count = 0
}

// StreamHandler is an slog.Handler that writes to both an underlying handler and a Broadcaster.
type StreamHandler struct {
	underlying  slog.Handler
	broadcaster *Broadcaster
	attrs       []slog.Attr
	groups      []string
}

// NewStreamHandler creates a StreamHandler wrapping an underlying slog.Handler.
func NewStreamHandler(underlying slog.Handler, broadcaster *Broadcaster) *StreamHandler {
	return &StreamHandler{
		underlying:  underlying,
		broadcaster: broadcaster,
	}
}

func (h *StreamHandler) Enabled(ctx context.Context, level slog.Level) bool {
	if h.underlying != nil {
		return h.underlying.Enabled(ctx, level)
	}
	return true
}

func (h *StreamHandler) Handle(ctx context.Context, record slog.Record) error {
	var underlyingErr error
	if h.underlying != nil {
		underlyingErr = h.underlying.Handle(ctx, record)
	}

	if h.broadcaster != nil {
		levelStr := "INFO"
		switch {
		case record.Level < slog.LevelInfo:
			levelStr = "DEBUG"
		case record.Level < slog.LevelWarn:
			levelStr = "INFO"
		case record.Level < slog.LevelError:
			levelStr = "WARN"
		default:
			levelStr = "ERROR"
		}

		var sb strings.Builder
		sb.WriteString(record.Message)

		// Collect attributes
		attrs := make([]string, 0)
		record.Attrs(func(attr slog.Attr) bool {
			if attr.Key == "level_tag" && attr.Value.String() == "SUCCESS" {
				levelStr = "SUCCESS"
				return true
			}
			attrs = append(attrs, fmt.Sprintf("%s=%v", attr.Key, attr.Value.Any()))
			return true
		})
		for _, a := range h.attrs {
			if a.Key == "level_tag" && a.Value.String() == "SUCCESS" {
				levelStr = "SUCCESS"
				continue
			}
			attrs = append(attrs, fmt.Sprintf("%s=%v", a.Key, a.Value.Any()))
		}

		if len(attrs) > 0 {
			sb.WriteString(" [")
			sb.WriteString(strings.Join(attrs, " "))
			sb.WriteString("]")
		}

		entry := LogEntry{
			Timestamp: record.Time.UTC().Format(time.RFC3339Nano),
			Level:     levelStr,
			Message:   sb.String(),
		}
		h.broadcaster.Add(entry)
	}

	return underlyingErr
}

func (h *StreamHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	return &StreamHandler{
		underlying:  h.underlying.WithAttrs(attrs),
		broadcaster: h.broadcaster,
		attrs:       append(append([]slog.Attr(nil), h.attrs...), attrs...),
		groups:      h.groups,
	}
}

func (h *StreamHandler) WithGroup(name string) slog.Handler {
	return &StreamHandler{
		underlying:  h.underlying.WithGroup(name),
		broadcaster: h.broadcaster,
		attrs:       h.attrs,
		groups:      append(append([]string(nil), h.groups...), name),
	}
}
