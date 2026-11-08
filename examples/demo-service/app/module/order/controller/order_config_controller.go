package controller

import (
	"context"

	"gofy/gofy-interface-service/appinterface/demoservice/orderrequest"
	"gofy/gofy-interface-service/appinterface/demoservice/orderresponse"
)

// GetConfig returns the demo order configuration. The value is a fixed sample;
// the controller deliberately reads no module config or auth token.
func (c *OrderController) GetConfig(_ context.Context, _ *orderrequest.GetConfigRequest) (*orderresponse.GetConfigResponse, error) {
	c.logger.Debug("get order config")
	return &orderresponse.GetConfigResponse{AuthRequired: false}, nil
}
