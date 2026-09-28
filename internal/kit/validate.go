package kit

import (
	"fmt"
	"math"
	"sort"
)

// ValidateArgs checks args against the tool's JSON Schema before the host
// runs it. Every caller (built-in agent, MCP bridge, later external kits)
// funnels through it, so a tool never sees arguments that violate its
// declared contract — a prerequisite for trusting kits that are not compiled
// into the binary.
//
// It implements the pragmatic subset the built-in schemas use: object type,
// per-property types (string / integer / number / boolean / array / object)
// and required keys. Unknown keys are allowed (the schemas declare no
// additionalProperties), which keeps future arguments forward-compatible.
func (t Tool) ValidateArgs(args map[string]any) error {
	schema := t.Schema
	if schema == nil {
		return nil
	}
	if typ, _ := schema["type"].(string); typ != "" && typ != "object" {
		// Defensive: kits declare object schemas; anything else is left to
		// the tool itself rather than rejected here.
		return nil
	}

	props, _ := schema["properties"].(map[string]any)
	if required, ok := schema["required"].([]string); ok {
		for _, name := range required {
			if _, present := args[name]; !present {
				return fmt.Errorf("参数缺少必填项: %s", name)
			}
		}
	} else if required, ok := schema["required"].([]any); ok {
		for _, name := range required {
			if s, isStr := name.(string); isStr {
				if _, present := args[s]; !present {
					return fmt.Errorf("参数缺少必填项: %s", s)
				}
			}
		}
	}

	names := make([]string, 0, len(args))
	for name := range args {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		decl, ok := props[name]
		if !ok {
			continue // unknown keys are allowed
		}
		if err := validateValue(name, decl, args[name]); err != nil {
			return err
		}
	}
	return nil
}

// validateValue checks one argument against its declared property schema.
func validateValue(name string, decl, value any) error {
	m, ok := decl.(map[string]any)
	if !ok {
		return nil
	}
	if value == nil {
		// JSON null: only meaningful when explicitly declared; leave it to the
		// tool so optional nulls stay usable.
		return nil
	}
	typ, _ := m["type"].(string)
	if typ == "" {
		return nil
	}
	bad := func(want string) error {
		return fmt.Errorf("参数类型错误: %s 应为 %s", name, want)
	}
	switch typ {
	case "string":
		if _, ok := value.(string); !ok {
			return bad("string")
		}
	case "integer":
		if !isInteger(value) {
			return bad("integer")
		}
	case "number":
		switch value.(type) {
		case float64, int, int64:
		default:
			return bad("number")
		}
	case "boolean":
		if _, ok := value.(bool); !ok {
			return bad("boolean")
		}
	case "array":
		if _, ok := value.([]any); !ok {
			return bad("array")
		}
	case "object":
		if _, ok := value.(map[string]any); !ok {
			return bad("object")
		}
	}
	return nil
}

// isInteger reports whether the decoded JSON value is an integral number.
// json.Unmarshal produces float64; in-process callers may pass int.
func isInteger(v any) bool {
	switch n := v.(type) {
	case float64:
		return n == math.Trunc(n) && !math.IsInf(n, 0) && !math.IsNaN(n)
	case int:
		return true
	case int64:
		return true
	default:
		return false
	}
}
