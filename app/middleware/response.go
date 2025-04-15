package middleware

import (
	"net/http"
	"reflect"

	"github.com/gin-contrib/requestid"
	"github.com/gin-gonic/gin"
)

// Response returnJson 响应结构体
type Response struct {
	Code  int    `json:"code"`
	Msg   string `json:"msg"`
	Data  any    `json:"data"`
	ReqId string `json:"req_id"`
}

// NewResponse 创建响应结构体
func NewResponse() *Response {
	return &Response{}
}

func (response *Response) Write(code int, data any, msg string) gin.HandlerFunc {
	valueType := reflect.ValueOf(data).Kind()
	switch valueType {
	case reflect.Slice:
		fallthrough
	case reflect.Map:
		if reflect.ValueOf(data).Len() == 0 {
			data = make([]any, 0)
		}
	default:
	}
	// 响应结构体
	response.Code = code
	response.Msg = msg
	response.Data = data

	return func(ctx *gin.Context) {
		response.ReqId = requestid.Get(ctx)
		ctx.JSON(http.StatusOK, response)
	}
}

func (response *Response) ReturnJson(ctx *gin.Context, code int, data any, msg string) {
	response.Write(code, data, msg)(ctx)
}
