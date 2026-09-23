package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"edgekit/internal/workspace"
)

// OpenAI-compatible chat message.
type chatMessage struct {
	Role       string     `json:"role"`
	Content    string     `json:"content,omitempty"`
	ToolCalls  []toolCall `json:"tool_calls,omitempty"`
	ToolCallID string     `json:"tool_call_id,omitempty"`
	Name       string     `json:"name,omitempty"`
}

type toolCall struct {
	ID       string `json:"id"`
	Type     string `json:"type"`
	Function struct {
		Name      string `json:"name"`
		Arguments string `json:"arguments"`
	} `json:"function"`
}

type toolSchema struct {
	Type     string `json:"type"`
	Function struct {
		Name        string         `json:"name"`
		Description string         `json:"description"`
		Parameters  map[string]any `json:"parameters"`
	} `json:"function"`
}

type chatRequest struct {
	Model    string        `json:"model"`
	Messages []chatMessage `json:"messages"`
	Tools    []toolSchema  `json:"tools,omitempty"`
}

type chatResponse struct {
	Choices []struct {
		Message chatMessage `json:"message"`
	} `json:"choices"`
	Error *struct {
		Message string `json:"message"`
	} `json:"error"`
}

// runLLM drives the function-calling loop against the configured model.
func (m *Manager) runLLM(ctx context.Context) {
	const maxSteps = 8
	m.emit(Event{Kind: KindStatus, Text: "AI 模式"})
	for step := 0; step < maxSteps; step++ {
		reply, err := m.chat(ctx)
		if err != nil {
			m.emit(Event{Kind: KindError, Text: err.Error()})
			return
		}
		if reply.Content != "" {
			m.emit(Event{Kind: KindAssistant, Text: reply.Content})
		}
		if len(reply.ToolCalls) == 0 {
			m.appendMessage(chatMessage{Role: "assistant", Content: reply.Content})
			return
		}
		m.appendMessage(chatMessage{Role: "assistant", Content: reply.Content, ToolCalls: reply.ToolCalls})
		for _, tc := range reply.ToolCalls {
			var args map[string]any
			if tc.Function.Arguments != "" {
				if err := json.Unmarshal([]byte(tc.Function.Arguments), &args); err != nil {
					args = nil
				}
			}
			out, _ := m.runTool(ctx, tc.Function.Name, args, false)
			m.appendMessage(chatMessage{
				Role:       "tool",
				ToolCallID: tc.ID,
				Name:       tc.Function.Name,
				Content:    truncate(out, 8000),
			})
		}
	}
	m.emit(Event{Kind: KindAssistant, Text: "（已达到最大工具调用步数，请补充信息或简化任务）"})
}

// chat performs one non-streaming chat completion.
func (m *Manager) chat(ctx context.Context) (*chatMessage, error) {
	cfg := m.Config()
	base := strings.TrimRight(cfg.BaseURL, "/")
	if base == "" {
		base = "https://api.openai.com/v1"
	}
	body, err := json.Marshal(chatRequest{
		Model:    cfg.Model,
		Messages: m.snapshot(m.systemPrompt()),
		Tools:    m.toolSchemas(),
	})
	if err != nil {
		return nil, err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, base+"/chat/completions", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+cfg.APIKey)

	client := &http.Client{Timeout: 90 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("调用模型失败: %w", err)
	}
	defer resp.Body.Close()

	data, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("模型返回 %d: %s", resp.StatusCode, truncate(string(data), 400))
	}
	var out chatResponse
	if err := json.Unmarshal(data, &out); err != nil {
		return nil, fmt.Errorf("解析模型响应失败: %w", err)
	}
	if out.Error != nil {
		return nil, fmt.Errorf("模型错误: %s", out.Error.Message)
	}
	if len(out.Choices) == 0 {
		return nil, fmt.Errorf("模型未返回内容")
	}
	return &out.Choices[0].Message, nil
}

func (m *Manager) toolSchemas() []toolSchema {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]toolSchema, 0, len(m.tools))
	for _, t := range m.tools {
		var ts toolSchema
		ts.Type = "function"
		ts.Function.Name = t.name
		ts.Function.Description = t.description
		ts.Function.Parameters = t.schema
		out = append(out, ts)
	}
	return out
}

func (m *Manager) systemPrompt() string {
	var b strings.Builder
	b.WriteString("你是 EdgeKit 的端侧设备智能助手，通过串口、SSH、工作区等工具帮助用户调试与升级端侧设备。\n")
	b.WriteString("只读工具会自动执行；写串口、执行命令、上传文件等修改性操作会先请求用户确认。\n")
	b.WriteString("回答使用简体中文，先给结论再给必要细节，命令输出可适当摘要。\n\n")
	fmt.Fprintf(&b, "用户当前选择的执行目标：%s。\n", targetLabel(m.Target()))
	b.WriteString("Remote 用 ssh_exec / serial_exec，Local 用 local_exec；\n")
	b.WriteString("若用户明确说「本机 / local」或「远端 / 设备 / remote」，以用户所说为准。\n\n当前环境：\n")
	if m.deps.Serial != nil && m.deps.Serial.IsOpen() {
		fmt.Fprintf(&b, "- 串口已打开: %s\n", m.deps.Serial.Port())
	} else {
		b.WriteString("- 串口未打开\n")
	}
	if m.deps.SSH != nil && m.deps.SSH.IsConnected() {
		fmt.Fprintf(&b, "- SSH 已连接: %s\n", m.deps.SSH.Target())
	} else {
		b.WriteString("- SSH 未连接\n")
	}
	if m.deps.SFTP != nil && m.deps.SFTP.IsConnected() {
		b.WriteString("- SFTP 已连接（可直接下载远端文件到工作区）\n")
	} else {
		b.WriteString("- SFTP 未就绪\n")
	}
	if root, err := workspace.Root(); err == nil {
		fmt.Fprintf(&b, "- 本地工作区: %s\n", root)
	}
	return b.String()
}
