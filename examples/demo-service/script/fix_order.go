package script

import (
	"context"

	"github.com/bingcool/gofy/apperr"
	gofyscript "github.com/bingcool/gofy/script"
	"github.com/spf13/pflag"
	appdb "github.com/yourorg/demo-service/app/io/db"
	"github.com/yourorg/demo-service/app/module/order/repository"
	orderservice "github.com/yourorg/demo-service/app/module/order/service"
	"go.uber.org/zap"
)

// FixOrder repairs inconsistent order status.
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

			if rt.DB == nil {
				return apperr.Internal("database is not configured")
			}
			// 脚本模式不执行 app/io 生命周期，仓库按 app/io 句柄取连接，
			// 因此把脚本连接发布到句柄，结束后恢复原值。
			prev := appdb.Db
			appdb.Db = rt.DB
			defer func() { appdb.Db = prev }()
			rows, err := (&repository.OrderRepository{}).CancelPending(ctx, id, orderservice.StatusPending, orderservice.StatusCancelled)
			if err != nil {
				return apperr.Internal("fix order failed").WithCause(err)
			}
			rt.Logger.Info("order fixed", zap.Uint64("order_id", id), zap.Int64("rows", rows))
			return nil
		},
	}
}
