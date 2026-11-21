package group3

import (
	"context"
	"fmt"
	"time"

	"go.uber.org/zap"

	supportai "github.com/bingcool/gofy/support/ai"
	gofyworker "github.com/bingcool/gofy/worker"
)

// AICheckConfig paces the live LLM check. No yaml tags: the values belong to
// the loop and live beside it, like group2's summary defaults.
// AICheckConfig 控制 LLM 巡检节奏。没有 yaml 标签：这些值是循环自己的属性，就和
// group2 的汇总参数一样写在旁边。
type AICheckConfig struct {
	// IntervalMS is the pause between two rounds, in milliseconds. One round
	// calls every configured alias, so the cost of a tick scales with the
	// number of models the deployment declares.
	// IntervalMS 是两轮之间的间隔，单位毫秒。一轮会调用所有已配置的 alias，
	// 因此一个 tick 的成本随部署声明的模型数量增长。
	IntervalMS uint32

	// Prompt is fixed text on purpose: the check answers "is the endpoint
	// reachable and is the credential accepted", not a real question.
	// Prompt 刻意是固定文本：这个巡检回答的是"端点通不通、凭证认不认"，不是一个真问题。
	Prompt string

	// MaxOutputTokens keeps the billed completion short.
	// MaxOutputTokens 让计费的 completion 尽量短。
	MaxOutputTokens int

	// Timeout bounds one call. It is a call-level deadline, not a connection
	// timeout, so a slow endpoint shows up as a failed check instead of a
	// worker that never finishes.
	// Timeout 限制单次调用。它是调用级 deadline 而不是连接超时，因此慢端点表现为
	// 一次失败的巡检，而不是一个永远不结束的 worker。
	Timeout time.Duration
}

// aiCheckDefaults calls every alias every ten minutes with a one-line prompt.
// Faster would burn quota for a signal that changes at deploy time, slower
// would let a broken credential sit unnoticed for too long.
// aiCheckDefaults 每十分钟用一句话提示词调用一次所有 alias。更快只会在部署期才变化的
// 信号上烧额度，更慢则会让失效的凭证藏太久。
var aiCheckDefaults = AICheckConfig{
	IntervalMS:      600_000,
	Prompt:          "用一句话说明幂等是什么",
	MaxOutputTokens: 64,
	Timeout:         30 * time.Second,
}

// newAICheckWorker calls every configured alias on a ticker and logs each
// answer with its token counts. Iterating the aliases instead of naming one is
// what makes the check cover both providers a deployment declares — the native
// deepseek client and the openai_compatible path — without a second worker or a
// second switch. A failure is logged, not returned: a bad key or an endpoint
// outage must not kill the worker whose job is to report it, and the next round
// retries.
//
// The client is built inside the run function rather than in NewGroup. Register
// runs in the HTTP process too, and that process must not construct — or
// validate — an LLM client it will never use.
// newAICheckWorker 按 ticker 调用所有已配置的 alias，并把每个回答与 token 数写进日志。
// 遍历 alias 而不是点名一个，正是这个巡检能同时覆盖部署声明的两个 provider——原生
// deepseek 客户端与 openai_compatible 路径——的原因，既不需要第二个 worker，也不需要
// 第二个开关。失败只记日志、不返回错误：坏 key 或端点故障不该杀掉负责报告它的 worker，
// 下一轮会重试。
//
// 客户端在 run 函数里构造，而不是在 NewGroup 里：Register 也在 HTTP 进程里执行，
// 那个进程不该构造——更不该校验——一个它永远不会用到的 LLM 客户端。
func newAICheckWorker(llm supportai.Config, cfg AICheckConfig) gofyworker.Worker {
	interval := cfg.IntervalMS
	if interval == 0 {
		interval = aiCheckDefaults.IntervalMS
	}
	prompt := cfg.Prompt
	if prompt == "" {
		prompt = aiCheckDefaults.Prompt
	}
	maxTokens := cfg.MaxOutputTokens
	if maxTokens == 0 {
		maxTokens = aiCheckDefaults.MaxOutputTokens
	}
	timeout := cfg.Timeout
	if timeout <= 0 {
		timeout = aiCheckDefaults.Timeout
	}

	return gofyworker.Func("ai-check", 1, func(ctx context.Context) error {
		rt, ok := gofyworker.RuntimeFrom(ctx)
		lg := zap.NewNop()
		if ok && rt.Logger != nil {
			lg = rt.Logger
		}

		client, err := supportai.New(llm, supportai.WithLogger(lg))
		if err != nil {
			return fmt.Errorf("ai check: %w", err)
		}
		defer func() { _ = client.Close() }()

		aliases := client.Aliases()
		lg.Info("ai check started",
			zap.Uint32("interval_ms", interval),
			zap.String("default_model", client.Default()),
			zap.Strings("models", aliases),
		)
		defer lg.Info("ai check stopped")

		// One call per alias, in the sorted order Aliases() returns. Each alias
		// gets its own deadline and its own log line: a broken compat endpoint
		// must not hide the native client's answer, and the alias is the field
		// an operator needs when one of several models fails.
		// 每个 alias 一次调用，顺序就是 Aliases() 的排序结果。每个 alias 各有自己的
		// deadline 与日志行：坏掉的 compat 端点不该掩盖原生客户端的回答，而"哪个模型
		// 失败"正是运维需要在日志里看到的字段。
		check := func(ctx context.Context) {
			for _, alias := range aliases {
				// DrainContext keeps an in-flight call alive through a shutdown
				// so the log line records the real outcome instead of a
				// cancellation.
				// DrainContext 让在途调用跨过停机，日志里记的是真实结果而不是被取消。
				callCtx, cancel := context.WithTimeout(gofyworker.DrainContext(ctx), timeout)

				started := time.Now()
				resp, err := client.Chat(callCtx, supportai.ChatRequest{
					ModelAlias:      alias,
					Prompt:          prompt,
					MaxOutputTokens: maxTokens,
				})
				cancel()
				if err != nil {
					lg.Warn("ai check failed",
						zap.String("alias", alias),
						zap.Error(err),
						zap.Duration("elapsed", time.Since(started)),
					)
					continue
				}
				// The answer is logged here on purpose: this is a demo check
				// whose whole point is to show the text and the billing it
				// produced. support/ai itself stays quiet — log_prompt /
				// log_completion are off by default there.
				// 这里刻意把回答写进日志：这是 demo 巡检，重点就是展示文本与它产生的计费。
				// support/ai 自己保持安静——它的 log_prompt / log_completion 默认关闭。
				lg.Info("ai check ok",
					zap.String("alias", alias),
					zap.String("text", resp.Text),
					zap.Int("input_tokens", resp.Usage.InputTokens),
					zap.Int("output_tokens", resp.Usage.OutputTokens),
					zap.Duration("elapsed", time.Since(started)),
				)
			}
		}

		// One call before the ticker: worker.Ticker waits a whole interval
		// before its first tick, and a check that stays silent for ten minutes
		// is indistinguishable from one that is broken.
		// 进入 ticker 之前先跑一次：worker.Ticker 要等满一个周期才触发，而一个十分钟
		// 不出声的巡检和一个坏掉的巡检看起来完全一样。
		check(ctx)

		return gofyworker.Ticker(ctx, interval, func(ctx context.Context) error {
			check(ctx)
			return nil
		})
	})
}
