package demoserviceclient

import (
	"context"

	"github.com/bingcool/gofy-interface-api/client"
	"github.com/yourorg/interface-service/appinterface/demoservice/orderrequest"
	"github.com/yourorg/interface-service/appinterface/demoservice/orderresponse"
	"github.com/yourorg/interface-service/appinterface/demoservice/orderroute"
)

// GetConfig returns the order module configuration.
// GetConfig 查询订单模块配置，返回诸如是否要求 Bearer Token 的开关；路由定义
// 在 orderroute.GetConfig，需认证。
func (c *Client) GetConfig(ctx context.Context, req *orderrequest.GetConfigRequest, opts ...client.CallOption) (*orderresponse.GetConfigResponse, error) {
	return call(ctx, c.c, orderroute.GetConfig, orderroute.Config.Prefix, req, opts)
}
