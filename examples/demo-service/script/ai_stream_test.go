package script

import (
	"net/http/httptest"
	"strings"
	"testing"
)

// TestWriteSSEKeepsPayloadOnOneLine pins the framing rule the sample depends on:
// SSE frames are newline-delimited, so a delta containing a blank line must be
// JSON-escaped rather than passed through, or one answer arrives as several
// broken frames.
// TestWriteSSEKeepsPayloadOnOneLine 钉住样依赖的帧格式规则：SSE 帧以换行分隔，
// 含空行的增量必须被 JSON 转义而不是原样写出去，否则一段回答会被拆成几个坏帧。
func TestWriteSSEKeepsPayloadOnOneLine(t *testing.T) {
	// ResponseRecorder implements http.Flusher, so it stands in for a real writer.
	// ResponseRecorder 实现了 http.Flusher，可以直接当真实 writer 用。
	rec := httptest.NewRecorder()
	writeSSE(rec, rec, "delta", map[string]string{"text": "第一行\n\n第二行"})

	body := rec.Body.String()
	if !strings.HasPrefix(body, "event: delta\n") {
		t.Fatalf("body must start with the event name, got %q", body)
	}
	if !strings.HasSuffix(body, "\n\n") {
		t.Fatalf("event must end with a blank line, got %q", body)
	}
	if got := strings.Count(body, "data: "); got != 1 {
		t.Fatalf("data lines = %d, want 1: a raw blank line split the event; body=%q", got, body)
	}
	// The escaped form has to be there: a JSON payload never contains a literal
	// newline, so its presence means the encoding step was skipped.
	// 转义后的形式必须在：JSON payload 不含字面换行，出现字面换行就说明绕过了编码。
	if !strings.Contains(body, `\n\n`) {
		t.Fatalf("newlines must be escaped, got %q", body)
	}
}
