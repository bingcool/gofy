//go:build !linux && !darwin && !windows

package process

// processToken reports "unknown" on platforms without a start-time query the
// standard library can reach. Callers must treat an empty token as unknown and
// fall back to the liveness check instead of pretending the identity matched.
// processToken 在没有可用启动时间查询的平台上返回"未知"。调用方必须把空 token 当成
// 未知、退回存活检查，而不是假装身份匹配。
func processToken(int) string { return "" }
