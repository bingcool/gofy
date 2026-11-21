package tenant

import (
	"database/sql"
	"fmt"
	"testing"

	"gorm.io/gorm/clause"
)

// tenantIDValue is a named string type, the shape a project's own tenant type
// takes when it is not a plain string.
// tenantIDValue 是命名 string 类型，对应项目自定义租户类型的情形。
type tenantIDValue string

type tenantInt int

// showTenant renders the tenant whatever it binds. The old fmt.Sprint fallback
// accepted the display string as proof, which let it skip the tenant condition.
// showTenant 无论绑定什么都渲染成租户值。旧的 fmt.Sprint 兜底会把它当作证明，从而跳过
// 租户条件。
type showTenant string

func (showTenant) String() string { return "42" }

// hideTenant renders a non-tenant string while binding the tenant; ignoring the
// display must not turn this into a false negative either.
// hideTenant 绑定的就是租户值，渲染的却不是；忽略展示形态也不能把它误判为不相等。
type hideTenant string

func (hideTenant) String() string { return "not-the-tenant" }

// formattedTenant steers fmt.Sprint through fmt.Formatter, the second display
// path the old fallback consulted.
// formattedTenant 通过 fmt.Formatter 影响 fmt.Sprint，是旧兜底读取的第二条展示路径。
type formattedTenant string

func (formattedTenant) Format(s fmt.State, _ rune) { _, _ = s.Write([]byte("42")) }

// errorTenant steers fmt.Sprint through error.Error, the third display path.
// errorTenant 通过 error.Error 影响 fmt.Sprint，是第三条展示路径。
type errorTenant string

func (errorTenant) Error() string { return "42" }

// TestSameTenant pins how a caller's bound value is reduced to the tenant
// string. The tenant column's Go type belongs to the application, so the same
// tenant arrives under several types; what must never happen is another
// tenant's value being accepted. A false result only costs a redundant
// predicate, so the table is deliberately one-sided: unknown shapes must be
// false rather than optimistically true.
//
// The display-string rows are the security half: a type whose String, Format
// or Error renders the tenant while binding another value must be false. The
// inverse row keeps the fix from over-correcting — a non-tenant display does
// not hide a matching bound value.
// TestSameTenant 固定「调用方绑定值如何归一成租户字符串」。租户列的 Go 类型由应用决定，
// 同一个租户会以多种类型出现；绝不能发生的是把别的租户的值判成相等。返回 false 只多一个
// 谓词，因此这张表刻意是单边的：认不出的形态必须为 false，而不是乐观地判 true。
//
// 展示字符串几行是安全的一半：String、Format 或 Error 渲染出租户值、绑定值却是别的租户的
// 类型必须为 false。反向那一行防止修过头：展示值不是租户，也不能掩盖相等的绑定值。
func TestSameTenant(t *testing.T) {
	tenant := "42"
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
		{name: "float", val: 42.0, want: true},
		{name: "named string", val: tenantIDValue("42"), want: true},
		{name: "named int", val: tenantInt(42), want: true},
		{name: "pointer", val: &tenant, want: true},
		{name: "valid null string", val: sql.NullString{String: "42", Valid: true}, want: true},
		{name: "invalid null string", val: sql.NullString{}, want: false},
		{name: "nil", val: nil, want: false},
		{name: "stringer shows tenant, binds other", val: showTenant("43"), want: false},
		{name: "stringer shows other, binds tenant", val: hideTenant("42"), want: true},
		{name: "formatter shows tenant, binds other", val: formattedTenant("43"), want: false},
		{name: "error shows tenant, binds other", val: errorTenant("43"), want: false},
		{name: "unsupported struct", val: struct{ ID string }{ID: "42"}, want: false},
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
