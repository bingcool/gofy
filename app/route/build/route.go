package build

import (
	"reflect"
	"strings"

	"github.com/bingcool/gofy/app/middleware"
	"github.com/bingcool/gofy/src/request"
	"github.com/creasty/defaults"
	"github.com/gin-contrib/requestid"
	"github.com/gin-gonic/gin"
	"github.com/go-playground/validator/v10"
	"github.com/gogf/gf/v2/util/gconv"
	"go.uber.org/zap"
)

// use a single instance of Validate, it caches struct info
var validate *validator.Validate

type RouteInterface interface {
	BuildReq(ctx *gin.Context, req any)
	Response(ctx *gin.Context, res any, err error)
}

type Route struct {
}

// BuildReq 构建请求
func (route *Route) BuildReq(ctx *gin.Context, req any) {
	_ = defaults.Set(req)
	_ = ctx.ShouldBind(req)
	ctx.Set("req_params", gconv.Map(req))
	go func() {
		reqId := requestid.Get(ctx)
		reqUri := ctx.Request.RequestURI
		reqParams, exists := ctx.Get("req_params")
		if !exists {
			reqParams = make(map[string]any)
		}
		request.Log("请求"+reqUri, zap.String("req_id", reqId), zap.Any("req_params", reqParams))
	}()
	route.validateCustomError(req)
}

// Response 响应
func (route *Route) Response(ctx *gin.Context, res any, err error) {
	responseMiddleware := &middleware.Response{}
	if err != nil {
		responseMiddleware.ReturnJson(ctx, -1, nil, err.Error())
		return
	} else {
		responseMiddleware.ReturnJson(ctx, 0, gconv.Map(res), "success")
		return
	}
}

// validateCustomError 验证自定义错误message
func (route *Route) validateCustomError(req interface{}) {
	validate = validator.New()
	err := validate.Struct(req)
	errorMsg := ""
	if ve, ok := err.(validator.ValidationErrors); ok {
		// 反射获取结构体类型
		typ := reflect.TypeOf(req)
		if typ.Kind() == reflect.Ptr {
			typ = typ.Elem()
		}
		for _, fe := range ve {
			// 获取字段信息
			field, _ := typ.FieldByName(fe.Field())
			msgTag := field.Tag.Get("message")
			msgMap := route.parseMessageTag(msgTag)
			// 根据验证规则获取消息
			if msg, exists := msgMap[fe.Tag()]; exists {
				errorMsg = msg
			} else {
				// 默认错误消息
				errorMsg = fe.Error()
			}

			if errorMsg != "" {
				panic(errorMsg)
			}
		}
	}

}

// parseMessageTag 解析message标签
func (route *Route) parseMessageTag(msgTag string) map[string]string {
	msgMap := make(map[string]string)
	pairs := strings.Split(msgTag, ",")
	for _, pair := range pairs {
		kv := strings.SplitN(pair, ":", 2)
		if len(kv) == 2 {
			msgMap[strings.TrimSpace(kv[0])] = strings.TrimSpace(kv[1])
		}
	}
	return msgMap
}
