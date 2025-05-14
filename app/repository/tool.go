package repository

import (
	"unicode"
)

func FirstUpper(s string) string {
	for i, r := range s {
		return string(unicode.ToUpper(r)) + s[i+1:]
	}
	return ""
}
