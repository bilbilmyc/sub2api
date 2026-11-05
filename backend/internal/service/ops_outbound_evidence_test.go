package service

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/logger"
	"go.uber.org/zap"
)

func TestOpsSystemLogSinkOutboundEvidenceSlowDatabaseAndOverflow(t *testing.T) {
	entered, release := make(chan struct{}), make(chan struct{})
	repo := &opsRepoMock{BatchInsertSystemLogsFn: func(ctx context.Context, _ []*OpsInsertSystemLogInput) (int64, error) {
		select {
		case <-entered:
		default:
			close(entered)
		}
		select {
		case <-release:
			return 0, errors.New("synthetic database outage")
		case <-ctx.Done():
			return 0, ctx.Err()
		}
	}}
	sink := NewOpsSystemLogSink(repo)
	sink.queue = make(chan *logger.LogEvent, 2)
	sink.batchSize = 1
	sink.flushInterval = time.Millisecond
	sink.Start()
	logger.SetSink(sink)
	t.Cleanup(func() { logger.SetSink(nil); close(release); sink.Stop() })
	fields := map[string]any{"event": "outbound.http_attempt", "schema_version": int64(1), "account_id": int64(6), "client_request_id": "request", "upstream_url": "https://provider.example/v1/responses"}
	emit := func() { logger.WriteSinkEvent("info", "route.outbound", "outbound.http_attempt", fields) }
	emit()
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("sink worker did not enter repository")
	}
	done := make(chan struct{})
	go func() {
		for range 100 {
			emit()
		}
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("slow database blocked producers")
	}
	if sink.Health().DroppedCount == 0 {
		t.Fatal("full queue must drop observations and report the count")
	}
}

func TestOpsSystemLogSinkOutboundEvidenceOnly(t *testing.T) {
	sink := &OpsSystemLogSink{}
	valid := func() *logger.LogEvent {
		return &logger.LogEvent{
			Level: "info", Message: "outbound.http_attempt", Fields: map[string]any{
				"service": "sub2api", "env": "test",
				"component": "route.outbound", "event": "outbound.http_attempt", "schema_version": int64(1),
				"request_id": "request", "client_request_id": "client", "account_id": int64(23),
				"upstream_url": "https://provider.example/v1/responses", "upstream_request_id": "provider-id",
			},
		}
	}
	if !sink.shouldIndex(valid()) {
		t.Fatal("versioned metadata event not retained at info")
	}
	for _, tc := range []struct {
		name   string
		mutate func(*logger.LogEvent)
	}{
		{"unknown field", func(e *logger.LogEvent) { e.Fields["authorization"] = "must-not-persist" }},
		{"different event", func(e *logger.LogEvent) { e.Message = "arbitrary-info" }},
		{"different schema", func(e *logger.LogEvent) { e.Fields["schema_version"] = int64(2) }},
		{"fractional schema", func(e *logger.LogEvent) { e.Fields["schema_version"] = 1.1 }},
		{"wrong component", func(e *logger.LogEvent) { e.Fields["component"] = "route.outbound.more" }},
		{"debug level", func(e *logger.LogEvent) { e.Level = "debug" }},
		{"explicit skip", func(e *logger.LogEvent) { e.Fields[logger.OpsSystemLogSkipField] = true }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			event := valid()
			tc.mutate(event)
			if sink.shouldIndex(event) {
				t.Fatal("non-metadata event was retained")
			}
		})
	}
	if sink.shouldIndex(&logger.LogEvent{Level: "info", Component: "other"}) {
		t.Fatal("general info persistence was enabled")
	}
}

func TestOpsSystemLogSinkOutboundEvidenceLoggerInit(t *testing.T) {
	if err := logger.Init(logger.InitOptions{Level: "info", ServiceName: "sub2api", Environment: "evidence-test"}); err != nil {
		t.Fatal(err)
	}
	sink := &OpsSystemLogSink{queue: make(chan *logger.LogEvent, 2)}
	logger.SetSink(sink)
	t.Cleanup(func() { logger.SetSink(nil) })
	fields := []zap.Field{
		zap.String("component", "route.outbound"),
		zap.String("event", "outbound.http_attempt"),
		zap.Int("schema_version", 1),
		zap.String("request_id", "local-request"),
		zap.String("client_request_id", "client-request"),
		zap.Int64("account_id", 23),
		zap.String("upstream_url", "https://provider.example/v1/responses"),
	}
	// This passes through the real logger core, WriteLogEvent and shouldIndex.
	logger.L().Info("outbound.http_attempt", fields...)
	select {
	case event := <-sink.queue:
		if event.Fields["service"] != "sub2api" || event.Fields["env"] != "evidence-test" {
			t.Fatalf("logger.Init labels not present: %#v", event.Fields)
		}
		if event.Fields["account_id"] != int64(23) || event.Fields["schema_version"] != int64(1) {
			t.Fatalf("real zap scalar types differ: %#v", event.Fields)
		}
	default:
		t.Fatal("real logger event was rejected by shouldIndex")
	}
	logger.L().Info("outbound.http_attempt", append(fields, zap.String("request_body", "synthetic-private-body"))...)
	if len(sink.queue) != 0 {
		t.Fatal("real event with a non-metadata field was retained")
	}
}
