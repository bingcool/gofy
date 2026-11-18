package ai

import "strings"

// redacted is what a secret turns into. It keeps the position visible in a
// message ("key [redacted] rejected") without leaking any character of it.
// redacted 是密钥被替换后的文本。它保留了消息里的位置（"key [redacted] rejected"）
// 却不泄露密钥的任何一个字符。
const redacted = "[redacted]"

// minRedactableLen guards against mangling messages when a credential is a
// short string: replacing every "a" in an error would make it unreadable
// without protecting anything.
// minRedactableLen 防止凭证过短时把消息改得不可读：把错误里每个 "a" 都替换掉既
// 保护不了什么，又让人看不懂。
const minRedactableLen = 6

// redactedError hides known secrets in Error() while preserving the chain, so
// errors.Is / errors.As still reach the provider's APIError and its status
// code.
// redactedError 在 Error() 里隐藏已知密钥，同时保留错误链，因此 errors.Is /
// errors.As 仍能拿到 provider 的 APIError 与状态码。
type redactedError struct {
	err     error
	secrets []string
}

// Error returns the wrapped message with every known secret replaced.
// Error 返回替换掉全部已知密钥后的消息。
func (e *redactedError) Error() string {
	if e == nil || e.err == nil {
		return ""
	}
	msg := e.err.Error()
	for _, s := range e.secrets {
		if len(s) < minRedactableLen {
			continue
		}
		msg = strings.ReplaceAll(msg, s, redacted)
	}
	return msg
}

// Unwrap exposes the original error.
// Unwrap 暴露原始错误。
func (e *redactedError) Unwrap() error { return e.err }

// redactError wraps err so that any of secrets appearing in its message is
// replaced. Errors with no secret, or with no usable secret, are returned
// unchanged.
// redactError 包装 err，把消息里出现的 secrets 替换掉；没有密钥可替换时原样返回。
func redactError(err error, secrets []string) error {
	if err == nil || len(secrets) == 0 {
		return err
	}
	usable := false
	for _, s := range secrets {
		if len(s) >= minRedactableLen {
			usable = true
			break
		}
	}
	if !usable {
		return err
	}
	return &redactedError{err: err, secrets: secrets}
}

// redactValue is the log-side counterpart: it never returns any part of the
// input, only whether a value was present.
// redactValue 是日志侧的做法：不返回输入的任何一个字符，只表明有没有值。
func redactValue(v string) string {
	if v == "" {
		return ""
	}
	return redacted
}

// truncate collapses s to one line and cuts it to n runes, appending an
// ellipsis when something was dropped. Rune-based so a multi-byte character is
// never split in half.
// truncate 把 s 压成一行并截到 n 个字符，被截断时补省略号。按 rune 计数，
// 多字节字符不会被切成半个。
func truncate(s string, n int) string {
	if n <= 0 {
		return ""
	}
	s = strings.Join(strings.Fields(s), " ")
	runes := []rune(s)
	if len(runes) <= n {
		return s
	}
	return string(runes[:n]) + "..."
}
