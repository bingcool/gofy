package support

import (
	"fmt"
	"reflect"
	"strings"

	"github.com/bingcool/gofy/apperr"
	"github.com/go-playground/validator/v10"
)

// defaultValidationMessage is used when no message tag matches the failing
// rule, so existing clients keep seeing the documented message.
// defaultValidationMessage 在没有 message tag 命中失败规则时使用，保证已有客户端
// 始终看到文档承诺的提示文案。
const defaultValidationMessage = "invalid argument"

// NewValidator returns a validator with tag names derived from json/form/uri
// tags, so error details use the same keys as the API payload.
// NewValidator 返回 tag 名取自 json/form/uri 的 validator，使错误 details 的键与
// API 负载一致；任何对校验规则的修改都应在启动期完成。
func NewValidator() *validator.Validate {
	v := validator.New()
	v.RegisterTagNameFunc(func(field reflect.StructField) string {
		for _, tag := range []string{"json", "form", "uri"} {
			name := strings.Split(field.Tag.Get(tag), ",")[0]
			if name != "" && name != "-" {
				return name
			}
		}
		return field.Name
	})
	return v
}

// validationError converts validator failures into a 40001 error. Custom
// message tags are promoted to msg (joined with "; " when several fields
// fail) and keep their per-field entries in details, so clients can show a
// readable message without parsing the envelope.
// validationError 把校验失败转换为 40001 错误：自定义 message tag 提升到 msg
// （多个字段失败时以 "; " 连接），同时保留逐字段的 details，客户端无需解析
// envelope 即可展示可读提示。
func validationError(req any, err error) *apperr.Error {
	verrs, ok := err.(validator.ValidationErrors)
	if !ok {
		return apperr.InvalidArgument(defaultValidationMessage)
	}
	typ := reflect.TypeOf(req)
	for typ != nil && typ.Kind() == reflect.Ptr {
		typ = typ.Elem()
	}
	details := make(map[string]string, len(verrs))
	messages := make([]string, 0, len(verrs))
	for _, fe := range verrs {
		msg, custom := messageFor(typ, fe)
		details[fe.Field()] = msg
		if custom {
			messages = append(messages, msg)
		}
	}
	if len(details) == 0 {
		return apperr.InvalidArgument(defaultValidationMessage)
	}
	if len(messages) > 0 {
		return apperr.InvalidArgument(strings.Join(messages, "; ")).WithDetails(details)
	}
	return apperr.InvalidArgument(defaultValidationMessage).WithDetails(details)
}

// messageFor returns the message tag entry matching the failing rule and
// whether it was defined. The tag is resolved from the struct namespace so
// dive errors also pick up the item field's message.
// messageFor 返回与失败规则匹配的 message tag 条目及其是否已定义；它按 struct
// namespace 解析，因此 dive 产生的元素错误也能取到对应字段的提示。
func messageFor(typ reflect.Type, fe validator.FieldError) (string, bool) {
	if field, ok := fieldFor(typ, fe.StructNamespace()); ok {
		if msg := parseMessageTag(field.Tag.Get("message"))[fe.Tag()]; msg != "" {
			return msg, true
		}
	}
	return defaultMessage(fe), false
}

// fieldFor walks the validator struct namespace, e.g.
// "CreateOrderRequest.Items[0].SkuID", and returns the failing field.
// fieldFor 沿 validator 的 struct namespace 逐层定位失败字段；路径无法解析或
// 字段不存在时返回 false，由调用方回落到默认提示。
func fieldFor(typ reflect.Type, namespace string) (reflect.StructField, bool) {
	segments := strings.Split(namespace, ".")
	if typ == nil || len(segments) < 2 {
		return reflect.StructField{}, false
	}
	current := typ
	var matched reflect.StructField
	for _, segment := range segments[1:] {
		name := segment
		if i := strings.IndexByte(name, '['); i >= 0 {
			name = name[:i]
		}
		for current != nil && (current.Kind() == reflect.Ptr ||
			current.Kind() == reflect.Slice || current.Kind() == reflect.Array) {
			current = current.Elem()
		}
		if current == nil || current.Kind() != reflect.Struct {
			return reflect.StructField{}, false
		}
		field, ok := current.FieldByName(name)
		if !ok {
			return reflect.StructField{}, false
		}
		matched = field
		current = field.Type
	}
	if matched.Name == "" {
		return reflect.StructField{}, false
	}
	return matched, true
}

// defaultMessage renders a concise fallback without the struct name that
// validator includes in FieldError.Error().
// defaultMessage 生成简洁的兜底提示，去掉 validator 在 FieldError.Error() 中
// 附带的结构体名，避免把内部类型暴露给客户端。
func defaultMessage(fe validator.FieldError) string {
	if fe.Param() == "" {
		return fmt.Sprintf("%s failed on the '%s' rule", fe.Field(), fe.Tag())
	}
	return fmt.Sprintf("%s failed on the '%s' rule (%s)", fe.Field(), fe.Tag(), fe.Param())
}

// parseMessageTag parses `tag:message,tag:message` pairs.
// parseMessageTag 解析 `tag:message,tag:message` 形式的 message tag；格式不符的
// 条目会被跳过而不是报错，只有能识别的规则生效。
func parseMessageTag(tag string) map[string]string {
	if tag == "" {
		return nil
	}
	out := make(map[string]string)
	for _, pair := range strings.Split(tag, ",") {
		kv := strings.SplitN(pair, ":", 2)
		if len(kv) != 2 {
			continue
		}
		key := strings.TrimSpace(kv[0])
		if key == "" {
			continue
		}
		out[key] = strings.TrimSpace(kv[1])
	}
	return out
}
