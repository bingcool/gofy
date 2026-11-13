package support

import (
	"errors"
	"net/http"

	"github.com/bingcool/gofy/apperr"
	"github.com/creasty/defaults"
	"github.com/gin-gonic/gin"
)

// bindAndValidate applies defaults, binds path/query/body values and validates
// the request. All failures are returned as *apperr.Error so the responder can
// render them without leaking implementation details.
// 失败一律封装为 *apperr.Error，调用方只应按错误码渲染，不要依赖底层错误文本；
// 请求体超过 MaxBytes 时映射为 413，其余绑定错误统一为 400。每个请求只应调用
// 一次，重复调用会再次写入默认值。
func (e *engine) bindAndValidate(c *gin.Context, req any) error {
	if err := defaults.Set(req); err != nil {
		return apperr.InvalidArgument("invalid request")
	}
	if err := c.ShouldBindUri(req); err != nil {
		return bindError(err)
	}
	if err := c.ShouldBind(req); err != nil {
		return bindError(err)
	}
	if err := e.validator.Struct(req); err != nil {
		return validationError(req, err)
	}
	return nil
}

func bindError(err error) *apperr.Error {
	var maxBytes *http.MaxBytesError
	if errors.As(err, &maxBytes) {
		return apperr.New(41300, http.StatusRequestEntityTooLarge, "request entity too large")
	}
	return apperr.New(40002, http.StatusBadRequest, "invalid request")
}
