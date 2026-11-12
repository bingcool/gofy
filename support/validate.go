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
const defaultValidationMessage = "invalid argument"

// NewValidator returns a validator with tag names derived from json/form/uri
// tags, so error details use the same keys as the API payload.
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
func defaultMessage(fe validator.FieldError) string {
	if fe.Param() == "" {
		return fmt.Sprintf("%s failed on the '%s' rule", fe.Field(), fe.Tag())
	}
	return fmt.Sprintf("%s failed on the '%s' rule (%s)", fe.Field(), fe.Tag(), fe.Param())
}

// parseMessageTag parses `tag:message,tag:message` pairs.
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
