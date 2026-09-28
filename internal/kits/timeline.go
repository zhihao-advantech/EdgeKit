package kits

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	"edgekit/internal/kit"
	"edgekit/internal/timeline"
)

// timelineKit exposes the focused device's record: the agent can wait for a
// matching output (boot banner, prompt, error) instead of polling reads.
type timelineKit struct{ tl kit.Timeline }

func (timelineKit) Manifest() kit.Manifest {
	return kit.Manifest{
		ID:          "edgekit.kit.timeline",
		Name:        "Timeline",
		Version:     "0.1.0",
		License:     "Apache-2.0",
		Runtime:     "builtin",
		Activation:  []string{kit.DeviceKindEvent("serial"), kit.DeviceKindEvent("ssh")},
		Description: "等待设备时间线出现匹配的输出（串口接收 / SSH stdout 等）",
	}
}

// wait_for_output clamps.
const (
	waitDefaultTimeout  = 30 * time.Second
	waitMaxTimeout      = 120 * time.Second
	waitDefaultLookback = 500
	waitMaxLookback     = 5000
)

func (k timelineKit) Tools() []kit.Tool {
	return []kit.Tool{
		{
			Name: "wait_for_output",
			Description: "阻塞等待设备输出中出现匹配正则 pattern 的记录（串口 rx / SSH stdout 等）。" +
				"发送命令后等它回显/打印结果时使用，替代反复读取。" +
				"默认同时回看最近 " + fmt.Sprint(waitDefaultLookback) +
				" 条记录（lookback 可调），因此两次调用之间到达的输出不会错过；" +
				"超时返回最近几条记录以便判断设备状态。",
			Risk: kit.RiskRead,
			Schema: deviceObj(map[string]any{
				"pattern":    strType(),
				"channel":    strType(),
				"kind":       strType(),
				"timeout_ms": intType(),
				"lookback":   intType(),
			}, "pattern"),
			Call: func(ctx context.Context, args map[string]any) (string, error) {
				if k.tl == nil {
					return "", fmt.Errorf("当前没有设备会话（请先连接串口或 SSH）")
				}
				pattern := argString(args, "pattern")
				if strings.TrimSpace(pattern) == "" {
					return "", fmt.Errorf("pattern 不能为空")
				}
				re, err := regexp.Compile(pattern)
				if err != nil {
					return "", fmt.Errorf("正则表达式无效: %w", err)
				}
				channel := argString(args, "channel")
				var channels []string
				switch channel {
				case "":
					// Device output only: never match the agent's own audit
					// records (whose data quotes tool arguments).
					channels = []string{timeline.ChannelSerial, timeline.ChannelSSH}
				case timeline.ChannelSerial, timeline.ChannelSSH:
					channels = []string{channel}
				default:
					return "", fmt.Errorf("channel 无效: %q（可用: serial / ssh，留空表示串口+SSH）", channel)
				}
				kind := argString(args, "kind")

				timeout := time.Duration(argInt(args, "timeout_ms", int(waitDefaultTimeout/time.Millisecond))) * time.Millisecond
				if timeout <= 0 {
					timeout = waitDefaultTimeout
				}
				if timeout > waitMaxTimeout {
					timeout = waitMaxTimeout
				}
				lookback := argInt(args, "lookback", waitDefaultLookback)
				if lookback < 0 {
					lookback = 0
				}
				if lookback > waitMaxLookback {
					lookback = waitMaxLookback
				}

				// Output that arrived between the previous tool call and this
				// one matters as much as future output, so scan a bounded
				// lookback window first, then block for anything newer.
				// `last` is captured before the scan, so records appended in
				// between are still caught by Wait's own re-scan.
				last := k.tl.LastSeq(ctx)
				after := last
				if after > uint64(lookback) {
					after -= uint64(lookback)
				} else {
					after = 0
				}
				f := timeline.Filter{Channels: channels, Kind: kind, Pattern: re, AfterSeq: after}
				for _, r := range k.tl.Since(ctx, after, 0) {
					if f.Match(r) {
						return k.matchedReport(pattern, r), nil
					}
				}
				f.AfterSeq = last
				rec, err := k.tl.Wait(ctx, f, timeout)
				if err != nil {
					if errors.Is(err, timeline.ErrTimeout) {
						return k.timeoutReport(ctx, pattern, channel, kind, timeout), nil
					}
					return "", err
				}
				return k.matchedReport(pattern, rec), nil
			},
		},
	}
}

// matchedReport formats the record that satisfied the wait.
func (k timelineKit) matchedReport(pattern string, rec timeline.Record) string {
	var b strings.Builder
	fmt.Fprintf(&b, "已捕获匹配 %s（[%s/%s] seq=%d %s）：\n",
		pattern, rec.Channel, rec.Kind, rec.Seq, rec.Time.Format("15:04:05"))
	b.WriteString(kit.Truncate(strings.ToValidUTF8(string(rec.Data), "\uFFFD"), 4096))
	return b.String()
}

// timeoutReport explains a timeout and tails the newest records so the brain
// can see what the device actually printed and re-plan.
func (k timelineKit) timeoutReport(ctx context.Context, pattern, channel, kind string, timeout time.Duration) string {
	var b strings.Builder
	fmt.Fprintf(&b, "等待超时（%.0fs）：未出现匹配 %s 的记录（channel=%s kind=%s）。\n",
		timeout.Seconds(), pattern, channel, kind)
	records := k.tl.Since(ctx, 0, 4)
	if len(records) == 0 {
		b.WriteString("等待期间没有收到任何设备输出。")
		return b.String()
	}
	b.WriteString("最近的设备输出：\n")
	for _, r := range records {
		fmt.Fprintf(&b, "[%s/%s seq=%d] %s\n", r.Channel, r.Kind, r.Seq,
			kit.Truncate(strings.TrimSpace(strings.ToValidUTF8(string(r.Data), "\uFFFD")), 200))
	}
	return b.String()
}
