package server

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeSkill(t *testing.T, root, name, desc string) {
	t.Helper()
	dir := filepath.Join(root, name)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	body := "---\nname: " + name + "\ndescription: " + desc + "\n---\n\n# " + name + "\n"
	if err := os.WriteFile(filepath.Join(dir, skillFile), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestParseSkillMeta(t *testing.T) {
	name, desc := parseSkillMeta([]byte("---\nname: board\ndescription: \"调试板子\"\n---\n\nbody"))
	if name != "board" || desc != "调试板子" {
		t.Fatalf("parseSkillMeta = %q, %q", name, desc)
	}
	if n, _ := parseSkillMeta([]byte("# no frontmatter\n")); n != "" {
		t.Fatalf("expected empty name, got %q", n)
	}
}

// TestSkillsInstallRemoveViaProtocol drives the right-hand panel's skill flow:
// list → install → view → remove, backed by ~/.agents/skills.
func TestSkillsInstallRemoveViaProtocol(t *testing.T) {
	builtin := t.TempDir()
	writeSkill(t, builtin, "board", "板子调试")
	t.Setenv("EDGEKIT_SKILLS_DIR", builtin)

	_, c := startE2EServer(t)

	var v struct {
		Skills []skillView `json:"skills"`
	}
	decodeSkills(t, c.until("skills").Payload, &v) // initial snapshot
	if len(v.Skills) != 1 || !v.Skills[0].Builtin || v.Skills[0].Installed {
		t.Fatalf("initial skills = %+v", v.Skills)
	}

	c.send("skills.install", map[string]any{"name": "board"})
	decodeSkills(t, c.until("skills").Payload, &v)
	if len(v.Skills) != 1 || !v.Skills[0].Installed {
		t.Fatalf("after install = %+v", v.Skills)
	}
	if _, err := os.Stat(filepath.Join(agentsSkillsDir(), "board", skillFile)); err != nil {
		t.Fatalf("installed skill missing: %v", err)
	}

	c.send("skills.view", map[string]any{"name": "board"})
	content := c.until("skills.content")
	var cv struct {
		Name    string `json:"name"`
		Content string `json:"content"`
	}
	if err := json.Unmarshal(content.Payload, &cv); err != nil {
		t.Fatalf("decode content: %v", err)
	}
	if cv.Name != "board" || !strings.Contains(cv.Content, "# board") {
		t.Fatalf("content = %+v", cv)
	}

	c.send("skills.remove", map[string]any{"name": "board"})
	decodeSkills(t, c.until("skills").Payload, &v)
	if len(v.Skills) != 1 || v.Skills[0].Installed {
		t.Fatalf("after remove = %+v", v.Skills)
	}
}

func TestSkillsInstallUnknownAndBadName(t *testing.T) {
	t.Setenv("EDGEKIT_SKILLS_DIR", t.TempDir())
	_, c := startE2EServer(t)
	c.until("skills")

	c.send("skills.install", map[string]any{"name": "missing"})
	if msg := c.until("error"); msg.Type != "error" {
		t.Fatalf("expected error, got %s", msg.Type)
	}
	c.send("skills.install", map[string]any{"name": "../escape"})
	if msg := c.until("error"); msg.Type != "error" {
		t.Fatalf("expected error, got %s", msg.Type)
	}
}

func decodeSkills(t *testing.T, payload json.RawMessage, v any) {
	t.Helper()
	if err := json.Unmarshal(payload, v); err != nil {
		t.Fatalf("decode skills: %v", err)
	}
}
