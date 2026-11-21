package health

import (
	"context"
	"strings"
	"testing"
)

// TestNewHTTPKeepsCredentialsOutOfErrors pins the redaction rule from the design
// (§3.3): check errors are rendered in the /health snapshot, and a checker URL
// may carry a token in its query string. The error has to name the cause, not
// repeat the credential.
// TestNewHTTPKeepsCredentialsOutOfErrors 固定设计 §3.3 的脱敏规则：检查错误会渲染到
// /health 快照，而检查 URL 的 query 可能携带 token。错误必须说明原因，不能重复凭据。
func TestNewHTTPKeepsCredentialsOutOfErrors(t *testing.T) {
	// Nothing listens on port 1, so the transport fails immediately.
	// 端口 1 上没有任何监听，传输层会立即失败。
	checker, err := NewHTTP("billing", HTTPConfig{URL: "http://127.0.0.1:1/health?token=s3cret"})
	if err != nil {
		t.Fatalf("NewHTTP: %v", err)
	}
	checkErr := checker.Check(context.Background())
	if checkErr == nil {
		t.Fatal("Check against a closed port returned nil")
	}
	if strings.Contains(checkErr.Error(), "s3cret") {
		t.Fatalf("check error repeats the credential: %v", checkErr)
	}

	// The construction error for a rejected scheme is what an operator reads in
	// the startup log, so it drops the query too.
	// 拒绝 scheme 时的构造错误会出现在启动日志里，因此同样去掉 query。
	_, err = NewHTTP("billing", HTTPConfig{URL: "ftp://example.com/health?token=s3cret"})
	if err == nil {
		t.Fatal("NewHTTP accepted the ftp scheme")
	}
	if strings.Contains(err.Error(), "s3cret") {
		t.Fatalf("construction error repeats the credential: %v", err)
	}
	if !strings.Contains(err.Error(), "ftp://example.com/health") {
		t.Fatalf("construction error lost the endpoint: %v", err)
	}
}
