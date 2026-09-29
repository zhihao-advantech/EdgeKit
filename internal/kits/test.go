package kits

import (
	"context"
	"fmt"
	"strings"

	"edgekit/internal/kit"
	"edgekit/internal/testrun"
)

// testKit exposes the host's test runs to a brain: list saved cases and recent
// runs, run a case against a device session (connect → run → generate →
// archive) and read a report.
type testKit struct{ t kit.Test }

func (testKit) Manifest() kit.Manifest {
	return kit.Manifest{
		ID:          "edgekit.kit.test",
		Name:        "Test",
		Version:     "0.1.0",
		License:     "Apache-2.0",
		Runtime:     "builtin",
		Activation:  []string{kit.DeviceKindEvent("serial"), kit.DeviceKindEvent("ssh")},
		Description: "设备测试运行（连接 / 运行 / 生成 / 归档）与会话回归",
	}
}

func (k testKit) Tools() []kit.Tool {
	return []kit.Tool{
		{
			Name:        "test_list",
			Description: "列出工作区中已保存的测试定义（tests/*.test.json）与最近的运行记录",
			Risk:        kit.RiskRead,
			Schema:      obj(nil),
			Call: func(ctx context.Context, args map[string]any) (string, error) {
				if k.t == nil {
					return "", fmt.Errorf("测试能力不可用")
				}
				defs := k.t.Definitions()
				runs := k.t.Runs()
				var b strings.Builder
				b.WriteString("已保存的测试定义：\n")
				if len(defs) == 0 {
					b.WriteString("  (无，可在界面「测试会话」中保存)\n")
				}
				for _, d := range defs {
					fmt.Fprintf(&b, "  - %s\t%s\n", d.Name, d.Path)
				}
				b.WriteString("最近的运行：\n")
				if len(runs) == 0 {
					b.WriteString("  (无)\n")
				}
				for _, r := range runs {
					fmt.Fprintf(&b, "  - %s\t%s\t%s\n", r.ID, r.Name, r.Status)
				}
				return b.String(), nil
			},
		},
		{
			Name: "test_run",
			Description: "在目标设备会话上运行一次测试：连接 → 运行 → 生成 → 归档（归档到本地工作区）。" +
				"用 path 指定工作区里已保存的定义，用 script 运行工作区脚本（tests/*.sh），" +
				"或用 command/expect 直接给一次性检查；返回结论、逐项结果与归档路径。",
			Risk: kit.RiskMutate,
			Schema: deviceObj(map[string]any{
				"path":       strType(),
				"script":     strType(),
				"name":       strType(),
				"command":    strType(),
				"expect":     strType(),
				"timeout_ms": intType(),
				"exit_zero":  map[string]any{"type": "boolean"},
			}),
			Call: func(ctx context.Context, args map[string]any) (string, error) {
				if k.t == nil {
					return "", fmt.Errorf("测试能力不可用")
				}
				session := argString(args, "session")
				path := strings.TrimSpace(argString(args, "path"))
				script := strings.TrimSpace(argString(args, "script"))

				var def *testrun.Definition
				switch {
				case path != "":
					// run a saved definition
				case script != "":
					d := testrun.Definition{
						Name: argString(args, "name"),
						Checks: []testrun.Check{{
							Name:      argString(args, "name"),
							Script:    script,
							Expect:    argString(args, "expect"),
							ExitZero:  argBool(args, "exit_zero"),
							TimeoutMS: argInt(args, "timeout_ms", 60000),
						}},
					}
					if d.Name == "" {
						d.Name = script
					}
					def = &d
				default:
					command := argString(args, "command")
					expect := argString(args, "expect")
					if strings.TrimSpace(command) == "" && strings.TrimSpace(expect) == "" {
						return "", fmt.Errorf("请提供 path、script 或 command/expect")
					}
					d := testrun.Definition{
						Name: argString(args, "name"),
						Checks: []testrun.Check{{
							Name:      argString(args, "name"),
							Command:   command,
							Expect:    expect,
							ExitZero:  argBool(args, "exit_zero"),
							TimeoutMS: argInt(args, "timeout_ms", 10000),
						}},
					}
					def = &d
				}

				run, err := k.t.Run(ctx, session, path, def)
				if err != nil {
					return "", err
				}
				return formatRun(run), nil
			},
		},
		{
			Name:        "test_report",
			Description: "读取某次归档测试运行（run）的 Markdown 报告与结论",
			Risk:        kit.RiskRead,
			Schema:      obj(map[string]any{"run": strType()}, "run"),
			Call: func(ctx context.Context, args map[string]any) (string, error) {
				if k.t == nil {
					return "", fmt.Errorf("测试能力不可用")
				}
				id := strings.TrimSpace(argString(args, "run"))
				if id == "" {
					return "", fmt.Errorf("run 不能为空")
				}
				report, err := k.t.Report(id)
				if err != nil {
					return "", err
				}
				return report, nil
			},
		},
	}
}

// formatRun renders a finished run as a short agent-facing summary.
func formatRun(run testrun.Run) string {
	var b strings.Builder
	fmt.Fprintf(&b, "测试「%s」结论：%s\n", run.Name, run.ResultLabel())
	fmt.Fprintf(&b, "运行 ID：%s\n", run.ID)
	if run.ArchivedPath != "" {
		fmt.Fprintf(&b, "归档：%s\n", run.ArchivedPath)
	}
	if p := run.Phase(testrun.PhaseRun); p != nil {
		for _, c := range p.Checks {
			label := "通过"
			if c.Status == testrun.CheckFail {
				label = "失败"
			} else if c.Status == testrun.CheckSkip {
				label = "跳过"
			}
			fmt.Fprintf(&b, "  [%s] %s", label, c.Name)
			if c.Command != "" {
				fmt.Fprintf(&b, "：%s", c.Command)
			}
			if c.Err != "" {
				fmt.Fprintf(&b, "（%s）", c.Err)
			}
			b.WriteString("\n")
		}
	}
	fmt.Fprintf(&b, "\n报告：\n%s", run.PhaseOutput(testrun.PhaseGenerate))
	return b.String()
}

// argBool reads a boolean argument.
func argBool(args map[string]any, key string) bool {
	if v, ok := args[key].(bool); ok {
		return v
	}
	return false
}
