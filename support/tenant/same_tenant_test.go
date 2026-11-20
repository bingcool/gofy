package tenant

import (
	"database/sql"
	"testing"

	"gorm.io/gorm/clause"
)

// tenantIDValue is a named string type, the shape a project's own tenant type
// takes when it is not a plain string.
// tenantIDValue 是命名 string 类型，对应项目自定义租户类型的情形。
type tenantIDValue string

// TestSameTenant pins how a caller's bound value is reduced to the tenant
// string. The tenant column's Go type belongs to the application, so the same
// tenant arrives under several types; what must never happen is another
// tenant's value being accepted. A false result only costs a redundant
// predicate, so the table is deliberately one-sided: unknown shapes must be
// false rather than optimistically true.
// TestSameTenant 固定「调用方绑定值如何归一成租户字符串」。租户列的 Go 类型由应用决定，
// 同一个租户会以多种类型出现；绝不能发生的是把别的租户的值判成相等。返回 false 只多一个
// 谓词，因此这张表刻意是单边的：认不出的形态必须为 false，而不是乐观地判 true。
func TestSameTenant(t *testing.T) {
	cases := []struct {
		name string
		val  any
		want bool
	}{
		{name: "string", val: "42", want: true},
		{name: "other string", val: "43", want: false},
		{name: "empty string", val: "", want: false},
		{name: "bytes", val: []byte("42"), want: true},
		{name: "other bytes", val: []byte("43"), want: false},
		{name: "int", val: 42, want: true},
		{name: "int64", val: int64(42), want: true},
		{name: "uint64", val: uint64(42), want: true},
		{name: "named string", val: tenantIDValue("42"), want: true},
		{name: "valid null string", val: sql.NullString{String: "42", Valid: true}, want: true},
		{name: "invalid null string", val: sql.NullString{}, want: false},
		{name: "nil", val: nil, want: false},
		// A column reference is not a value: comparing it would need the row,
		// which the plugin does not have.
		// 列引用不是值：比较它需要那一行的数据，插件没有。
		{name: "column reference", val: clause.Column{Name: "42"}, want: false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := sameTenant(tc.val, "42"); got != tc.want {
				t.Fatalf("sameTenant(%#v, %q) = %v, want %v", tc.val, "42", got, tc.want)
			}
		})
	}
}
