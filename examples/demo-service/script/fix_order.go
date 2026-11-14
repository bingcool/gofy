package script

import (
	"context"

	"github.com/bingcool/gofy/apperr"
	gofyscript "github.com/bingcool/gofy/script"
	"github.com/spf13/pflag"
	"github.com/yourorg/demo-service/app/module/order/repository"
	orderservice "github.com/yourorg/demo-service/app/module/order/service"
	"go.uber.org/zap"
)

// FixOrder repairs inconsistent order status.
// FixOrder 修复不一致的订单状态：按 order-id 把待支付订单改成已取消，dry-run
// 时只打印不落库。
func FixOrder() gofyscript.Script {
	return gofyscript.Script{
		Name:  "fix-order",
		Short: "fix inconsistent order data",
		Flags: func(fs *pflag.FlagSet) {
			fs.Uint64("order-id", 0, "order id")
			fs.Bool("dry-run", false, "only print what would be changed")
		},
		Run: func(ctx context.Context, rt *gofyscript.Runtime, fs *pflag.FlagSet) error {
			id, _ := fs.GetUint64("order-id")
			dryRun, _ := fs.GetBool("dry-run")
			if id == 0 {
				return apperr.BadRequest("order-id is required")
			}
			rt.Logger.Info("fix order",
				zap.Uint64("order_id", id),
				zap.Bool("dry_run", dryRun),
			)
			if dryRun {
				rt.Logger.Info("fix order dry-run, no data changed", zap.Uint64("order_id", id))
				return nil
			}

			// Register 已在脚本启动时发布了本代 app/appio 快照，仓库按
			// WithContext 取到的就是脚本这一代的连接。
			rows, err := (&repository.OrderRepository{}).CancelPending(ctx, id, orderservice.StatusPending, orderservice.StatusCancelled)
			if err != nil {
				return apperr.Internal("fix order failed").WithCause(err)
			}
			rt.Logger.Info("order fixed", zap.Uint64("order_id", id), zap.Int64("rows", rows))
			return nil
		},
	}
}
