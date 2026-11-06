package script

import (
	"context"

	"github.com/bingcool/gofy/apperr"
	gofyscript "github.com/bingcool/gofy/script"
	"github.com/spf13/pflag"
	"github.com/yourorg/demo-service/app/config"
	"github.com/yourorg/demo-service/app/module/order/repository"
	"go.uber.org/zap"
)

// FixOrder repairs inconsistent order status.
func FixOrder(cfg *config.Config) gofyscript.Script {
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
				zap.Duration("payment_timeout", cfg.Order.PaymentTimeout),
			)
			if dryRun {
				rt.Logger.Info("fix order dry-run, no data changed", zap.Uint64("order_id", id))
				return nil
			}

			if rt.DB == nil {
				return apperr.Internal("database is not configured")
			}
			rows, err := repository.New(rt.DB, rt.Logger).CancelPending(ctx, id)
			if err != nil {
				return apperr.Internal("fix order failed").WithCause(err)
			}
			rt.Logger.Info("order fixed", zap.Uint64("order_id", id), zap.Int64("rows", rows))
			return nil
		},
	}
}
