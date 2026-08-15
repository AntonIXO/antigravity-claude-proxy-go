package logger

import (
	"bytes"
	"context"
	"log/slog"
	"strings"
	"testing"
	"time"
)

func TestBroadcaster_RingBuffer(t *testing.T) {
	b := NewBroadcaster(3)

	b.Add(LogEntry{Timestamp: "1", Level: "INFO", Message: "first"})
	b.Add(LogEntry{Timestamp: "2", Level: "WARN", Message: "second"})
	b.Add(LogEntry{Timestamp: "3", Level: "ERROR", Message: "third"})

	history := b.GetHistory()
	if len(history) != 3 {
		t.Fatalf("expected 3 entries, got %d", len(history))
	}
	if history[0].Message != "first" || history[2].Message != "third" {
		t.Errorf("unexpected history contents: %+v", history)
	}

	// Add 4th entry, which overflows and overwrites 1st
	b.Add(LogEntry{Timestamp: "4", Level: "INFO", Message: "fourth"})
	history = b.GetHistory()
	if len(history) != 3 {
		t.Fatalf("expected 3 entries, got %d", len(history))
	}
	if history[0].Message != "second" || history[1].Message != "third" || history[2].Message != "fourth" {
		t.Errorf("unexpected ring buffer ordering: %+v", history)
	}
}

func TestBroadcaster_Subscription(t *testing.T) {
	b := NewBroadcaster(10)
	ch, cancel := b.Subscribe(5)
	defer cancel()

	b.Add(LogEntry{Timestamp: "1", Level: "INFO", Message: "msg1"})
	b.Add(LogEntry{Timestamp: "2", Level: "INFO", Message: "msg2"})

	select {
	case entry := <-ch:
		if entry.Message != "msg1" {
			t.Errorf("expected msg1, got %s", entry.Message)
		}
	case <-time.After(100 * time.Millisecond):
		t.Fatal("timed out waiting for entry")
	}

	select {
	case entry := <-ch:
		if entry.Message != "msg2" {
			t.Errorf("expected msg2, got %s", entry.Message)
		}
	case <-time.After(100 * time.Millisecond):
		t.Fatal("timed out waiting for entry")
	}
}

func TestStreamHandler(t *testing.T) {
	var buf bytes.Buffer
	underlying := slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})
	broadcaster := NewBroadcaster(10)
	handler := NewStreamHandler(underlying, broadcaster)
	log := slog.New(handler)

	log.InfoContext(context.Background(), "test message", "key", "value")

	history := broadcaster.GetHistory()
	if len(history) != 1 {
		t.Fatalf("expected 1 log entry in broadcaster, got %d", len(history))
	}
	if history[0].Level != "INFO" {
		t.Errorf("expected level INFO, got %s", history[0].Level)
	}
	if !strings.Contains(history[0].Message, "test message") || !strings.Contains(history[0].Message, "key=value") {
		t.Errorf("unexpected entry message: %s", history[0].Message)
	}
	if !strings.Contains(buf.String(), "test message") {
		t.Errorf("expected underlying text handler to receive message")
	}
}

func TestStreamHandler_Success(t *testing.T) {
	broadcaster := NewBroadcaster(10)
	handler := NewStreamHandler(nil, broadcaster)
	log := slog.New(handler)

	Success(log, "operation succeeded", "account", "test@example.com")

	history := broadcaster.GetHistory()
	if len(history) != 1 {
		t.Fatalf("expected 1 log entry, got %d", len(history))
	}
	if history[0].Level != "SUCCESS" {
		t.Errorf("expected level SUCCESS, got %s", history[0].Level)
	}
	if strings.Contains(history[0].Message, "level_tag=") {
		t.Errorf("expected level_tag attribute to be stripped from message, got %s", history[0].Message)
	}
	if !strings.Contains(history[0].Message, "operation succeeded") || !strings.Contains(history[0].Message, "account=test@example.com") {
		t.Errorf("unexpected message: %s", history[0].Message)
	}
}

func TestLogSuccess(t *testing.T) {
	broadcaster := NewBroadcaster(10)
	handler := NewStreamHandler(nil, broadcaster)
	oldDefault := slog.Default()
	slog.SetDefault(slog.New(handler))
	defer slog.SetDefault(oldDefault)

	LogSuccess("global success", "detail", "ok")

	history := broadcaster.GetHistory()
	if len(history) != 1 {
		t.Fatalf("expected 1 log entry, got %d", len(history))
	}
	if history[0].Level != "SUCCESS" {
		t.Errorf("expected level SUCCESS, got %s", history[0].Level)
	}
	if strings.Contains(history[0].Message, "level_tag=") {
		t.Errorf("expected level_tag attribute to be stripped from message, got %s", history[0].Message)
	}
}
