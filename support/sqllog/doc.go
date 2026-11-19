// Package sqllog is the SQL statement log: text modes, masking, process-wide
// sampling and rate limits, and the GORM adapter shared by support/db and
// support/sqlite.
//
// It lives outside logger on purpose. A SQL entry carries X-Trace-Id and the
// OTel span from the request context, so this layer depends on
// support/headerpropagation and support/otel — while logger must stay a leaf
// (zap and rotation only), and the database packages must not import logger.
// Keeping the layer here satisfies all three import guards and makes the split
// explicit: log: decides where entries go, sql_log: decides what a SQL entry
// contains.
//
// 包 sqllog 是 SQL 语句日志：文本档位、脱敏、进程级采样与限流，以及 support/db 与
// support/sqlite 共用的 GORM 适配器。
//
// 它刻意不在 logger 里。一条 SQL 条目要带上 ctx 里的 X-Trace-Id 与 OTel Span，因此这一层
// 依赖 support/headerpropagation 与 support/otel；而 logger 必须保持叶子包（只依赖 zap
// 与轮转），数据库包也不得 import logger。放在这里三条导入守卫同时成立，归属也更清楚：
// log: 决定条目去哪，sql_log: 决定一条 SQL 条目记什么。
package sqllog
