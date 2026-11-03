package script

import (
	gofyscript "github.com/bingcool/gofy/script"
	"github.com/yourorg/order-service/app/config"
)

func Register(cfg *config.Config) []gofyscript.Script {
	return []gofyscript.Script{
		FixOrder(cfg),
	}
}
