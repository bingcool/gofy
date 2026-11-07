package support

import (
	"reflect"
	"strings"

	"github.com/bingcool/gofy/apperr"
	"github.com/go-playground/validator/v10"
)

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

func validationError(req any, err error) *apperr.Error {
	invalid := apperr.InvalidArgument("invalid argument")
	verrs, ok := err.(validator.ValidationErrors)
	if !ok {
		return invalid
	}
	details := make(map[string]string, len(verrs))
	typ := reflect.TypeOf(req)
	for typ != nil && typ.Kind() == reflect.Ptr {
		typ = typ.Elem()
	}
	for _, fe := range verrs {
		details[fe.Field()] = messageFor(typ, fe)
	}
	if len(details) == 0 {
		return invalid
	}
	return invalid.WithDetails(details)
}

func messageFor(typ reflect.Type, fe validator.FieldError) string {
	if typ != nil && typ.Kind() == reflect.Struct {
		if field, ok := typ.FieldByName(fe.StructField()); ok {
			if msg := parseMessageTag(field.Tag.Get("message"))[fe.Tag()]; msg != "" {
				return msg
			}
		}
	}
	return fe.Error()
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
