package responses

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/tidwall/gjson"
)

// ConvertCodexResponseToOpenAIResponses converts OpenAI Chat Completions streaming chunks
// to OpenAI Responses SSE events (response.*).

// Codex websocket and some upstream implementations can stream JSON payloads as `data:` lines
// without emitting an empty line between events. Strict SSE clients treat consecutive `data:`
// lines as a single event, joining them with `\n`, which then breaks JSON parsing.
//
// To ensure OpenAI Responses streaming compatibility, normalize each JSON payload into a
// self-contained SSE event chunk with an `event:` line matching the payload `type`, followed
// by a single `data:` line containing compact JSON.
type codexToOpenAIResponsesSSEState struct {
	PendingEvent string
}

func ConvertCodexResponseToOpenAIResponses(_ context.Context, _ string, _, _, rawJSON []byte, param *any) []string {
	if param != nil && *param == nil {
		*param = &codexToOpenAIResponsesSSEState{}
	}
	var st *codexToOpenAIResponsesSSEState
	if param != nil {
		if typed, ok := (*param).(*codexToOpenAIResponsesSSEState); ok {
			st = typed
		}
	}

	trimmed := bytes.TrimSpace(rawJSON)
	if len(trimmed) == 0 {
		return []string{}
	}

	if bytes.HasPrefix(trimmed, []byte("event:")) {
		// Capture the upstream event name in case payloads omit `type`.
		if st != nil {
			st.PendingEvent = strings.TrimSpace(string(bytes.TrimSpace(trimmed[len("event:"):])))
		}
		return []string{}
	}

	if !bytes.HasPrefix(trimmed, []byte("data:")) {
		// Ignore anything else (comments/heartbeats/unknown lines).
		return []string{}
	}

	payload := bytes.TrimSpace(trimmed[len("data:"):])
	if len(payload) == 0 {
		return []string{}
	}
	if bytes.Equal(payload, []byte("[DONE]")) {
		return []string{}
	}

	// Only normalize valid JSON. If the upstream ever emits non-JSON data lines, pass them through.
	if !json.Valid(payload) {
		return []string{fmt.Sprintf("data: %s", string(payload))}
	}

	// Compact JSON to guarantee the `data:` line stays single-line.
	compact := payload
	var buf bytes.Buffer
	if err := json.Compact(&buf, payload); err == nil && buf.Len() > 0 {
		compact = buf.Bytes()
	}

	eventType := strings.TrimSpace(gjson.GetBytes(compact, "type").String())
	if eventType == "" {
		if st != nil {
			eventType = strings.TrimSpace(st.PendingEvent)
			st.PendingEvent = ""
		}
		// Final fallback to the default SSE event name.
		if eventType == "" {
			eventType = "message"
		}
	}

	out := fmt.Sprintf("event: %s\ndata: %s", eventType, string(compact))
	return []string{out}
}

// ConvertCodexResponseToOpenAIResponsesNonStream builds a single Responses JSON
// from a non-streaming OpenAI Chat Completions response.
func ConvertCodexResponseToOpenAIResponsesNonStream(_ context.Context, _ string, _, _, rawJSON []byte, _ *any) string {
	rootResult := gjson.ParseBytes(rawJSON)
	// Verify this is a response.completed event
	if rootResult.Get("type").String() != "response.completed" {
		return ""
	}
	responseResult := rootResult.Get("response")
	return responseResult.Raw
}
