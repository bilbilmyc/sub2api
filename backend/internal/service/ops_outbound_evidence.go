package service

import "github.com/Wei-Shaw/sub2api/internal/pkg/logger"

// Persist only this versioned metadata event, without enabling general info logs.
func isOutboundEvidenceMetadata(event *logger.LogEvent) bool {
	if event == nil || event.Message != "outbound.http_attempt" || event.Fields == nil ||
		asString(event.Fields["event"]) != "outbound.http_attempt" {
		return false
	}
	// zap.Int is encoded as int64. Accept integer producers only; coercing a
	// fractional JSON number or a string could accidentally accept another schema.
	version := int64(0)
	switch value := event.Fields["schema_version"].(type) {
	case int:
		version = int64(value)
	case int64:
		version = value
	}
	if version != 1 {
		return false
	}
	for key := range event.Fields {
		switch key {
		case "service", "env", // labels added by logger.Init to every event
			"component", "event", "schema_version", "request_id", "client_request_id", "account_id", "attempt_id",
			"upstream_url", "upstream_host", "request_host", "effective_host", "proxy_host", "proxy_scheme",
			"status_code", "response_received", "upstream_request_id_header", "upstream_request_id",
			"upstream_request_id_truncated", "transport_error", "headers_elapsed_ms":
		default:
			return false
		}
	}
	return true
}
