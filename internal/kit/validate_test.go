package kit

import (
	"strings"
	"testing"
)

func strType() map[string]any { return map[string]any{"type": "string"} }
func intType() map[string]any { return map[string]any{"type": "integer"} }

func obj(props map[string]any, required ...string) map[string]any {
	m := map[string]any{"type": "object", "properties": props}
	if len(required) > 0 {
		m["required"] = required
	}
	return m
}

func TestValidateArgsRequiredAndTypes(t *testing.T) {
	tool := Tool{
		Name:   "demo",
		Schema: obj(map[string]any{"s": strType(), "n": intType()}, "s"),
	}
	cases := []struct {
		name string
		args map[string]any
		err  string
	}{
		{"ok minimal", map[string]any{"s": "x"}, ""},
		{"ok both", map[string]any{"s": "x", "n": 3}, ""},
		{"ok integer via json float", map[string]any{"s": "x", "n": 3.0}, ""},
		{"missing required", map[string]any{"n": 1}, "参数缺少必填项: s"},
		{"nil args missing required", nil, "参数缺少必填项: s"},
		{"string type wrong", map[string]any{"s": 42}, "参数类型错误: s 应为 string"},
		{"integer type wrong", map[string]any{"s": "x", "n": "3"}, "参数类型错误: n 应为 integer"},
		{"integer not integral", map[string]any{"s": "x", "n": 3.5}, "参数类型错误: n 应为 integer"},
		{"unknown keys allowed", map[string]any{"s": "x", "future": true}, ""},
		{"null allowed", map[string]any{"s": "x", "n": nil}, ""},
	}
	for _, tc := range cases {
		err := tool.ValidateArgs(tc.args)
		if tc.err == "" {
			if err != nil {
				t.Fatalf("%s: unexpected error %v", tc.name, err)
			}
			continue
		}
		if err == nil || !strings.Contains(err.Error(), tc.err) {
			t.Fatalf("%s: got %v, want %q", tc.name, err, tc.err)
		}
	}
}

func TestValidateArgsBooleanNumberArrayObject(t *testing.T) {
	tool := Tool{
		Name: "demo",
		Schema: obj(map[string]any{
			"b": map[string]any{"type": "boolean"},
			"f": map[string]any{"type": "number"},
			"a": map[string]any{"type": "array"},
			"o": map[string]any{"type": "object"},
		}),
	}
	ok := map[string]any{"b": true, "f": 1.5, "a": []any{1, 2}, "o": map[string]any{"k": "v"}}
	if err := tool.ValidateArgs(ok); err != nil {
		t.Fatalf("valid args rejected: %v", err)
	}
	bad := []map[string]any{
		{"b": "true"},
		{"f": "1.5"},
		{"a": map[string]any{}},
		{"o": []any{}},
	}
	for _, args := range bad {
		if err := tool.ValidateArgs(args); err == nil || !strings.Contains(err.Error(), "参数类型错误") {
			t.Fatalf("args %v should be rejected, got %v", args, err)
		}
	}
}

func TestValidateArgsPermissiveSchemas(t *testing.T) {
	noSchema := Tool{Name: "a", Schema: nil}
	if err := noSchema.ValidateArgs(map[string]any{"anything": 1}); err != nil {
		t.Fatalf("nil schema should not validate: %v", err)
	}
	notObject := Tool{Name: "b", Schema: map[string]any{"type": "string"}}
	if err := notObject.ValidateArgs(map[string]any{}); err != nil {
		t.Fatalf("non-object schema is left to the tool: %v", err)
	}
	emptyProps := Tool{Name: "c", Schema: obj(nil)}
	if err := emptyProps.ValidateArgs(nil); err != nil {
		t.Fatalf("no required, no props should accept nil args: %v", err)
	}
}

func TestValidateArgsRequiredViaJSONRoundTrip(t *testing.T) {
	// A schema that went through JSON (external kits later) has required as
	// []any, not []string.
	schema := map[string]any{
		"type":       "object",
		"properties": map[string]any{"s": map[string]any{"type": "string"}},
		"required":   []any{"s"},
	}
	tool := Tool{Name: "demo", Schema: schema}
	if err := tool.ValidateArgs(nil); err == nil || !strings.Contains(err.Error(), "参数缺少必填项: s") {
		t.Fatalf("required via []any should be enforced, got %v", err)
	}
}
