package server

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// The Agent skill surface is managed from the right-hand "MCP 接口" panel:
// built-in skills shipped with EdgeKit are listed alongside the ones installed
// under ~/.agents/skills, and can be installed / viewed / removed.

const skillFile = "SKILL.md"

// skillView is one Agent skill as the UI sees it.
type skillView struct {
	Name          string `json:"name"`
	Description   string `json:"description"`
	Builtin       bool   `json:"builtin"`   // shipped with EdgeKit
	Installed     bool   `json:"installed"` // present under ~/.agents/skills
	Path          string `json:"path,omitempty"`
	InstalledPath string `json:"installedPath,omitempty"`
}

// agentsSkillsDir is where an Agent looks for installed skills.
func agentsSkillsDir() string {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return filepath.Join(os.TempDir(), ".agents", "skills")
	}
	return filepath.Join(home, ".agents", "skills")
}

// builtinSkillDirs lists the read-only directories bundled skills may live in.
// EDGEKIT_SKILLS_DIR wins; the repository layout (cwd) and installed layouts
// (next to the executable, share/edgekit) are also probed.
func builtinSkillDirs() []string {
	var dirs []string
	if d := strings.TrimSpace(os.Getenv("EDGEKIT_SKILLS_DIR")); d != "" {
		dirs = append(dirs, d)
	}
	if wd, err := os.Getwd(); err == nil {
		dirs = append(dirs, filepath.Join(wd, "skills"))
	}
	if exe, err := os.Executable(); err == nil {
		e := filepath.Dir(exe)
		dirs = append(dirs,
			filepath.Join(e, "skills"),
			filepath.Join(e, "..", "share", "edgekit", "skills"),
			filepath.Join(e, "..", "lib", "edgekit", "skills"),
		)
	}
	return dirs
}

// validSkillName rejects names that could escape the skills directory.
func validSkillName(name string) bool {
	if name == "" || name == "." || name == ".." {
		return false
	}
	return !strings.ContainsAny(name, `/\`)
}

// scanSkillDir returns the skills in a directory laid out as <root>/<name>/SKILL.md.
func scanSkillDir(root string) []skillView {
	entries, err := os.ReadDir(root)
	if err != nil {
		return nil
	}
	out := make([]skillView, 0, len(entries))
	for _, e := range entries {
		name := e.Name()
		if !validSkillName(name) {
			continue
		}
		dir := filepath.Join(root, name)
		info, err := os.Stat(dir)
		if err != nil || !info.IsDir() {
			continue
		}
		data, err := os.ReadFile(filepath.Join(dir, skillFile))
		if err != nil {
			continue
		}
		metaName, desc := parseSkillMeta(data)
		if !validSkillName(metaName) {
			metaName = name
		}
		out = append(out, skillView{
			Name:        name,
			Description: truncateRunes(desc, 320),
			Path:        filepath.Join(dir, skillFile),
		})
	}
	return out
}

// parseSkillMeta reads the YAML frontmatter name / description of a SKILL.md.
func parseSkillMeta(data []byte) (name, desc string) {
	s := string(data)
	if !strings.HasPrefix(s, "---") {
		return "", ""
	}
	rest := strings.TrimPrefix(s, "---")
	if i := strings.Index(rest, "\n---"); i >= 0 {
		rest = rest[:i]
	}
	for _, line := range strings.Split(rest, "\n") {
		line = strings.TrimSpace(line)
		switch {
		case strings.HasPrefix(line, "name:"):
			name = unquote(strings.TrimSpace(strings.TrimPrefix(line, "name:")))
		case strings.HasPrefix(line, "description:"):
			desc = unquote(strings.TrimSpace(strings.TrimPrefix(line, "description:")))
		}
	}
	return name, desc
}

func unquote(s string) string {
	if len(s) >= 2 {
		if (s[0] == '"' && s[len(s)-1] == '"') || (s[0] == '\'' && s[len(s)-1] == '\'') {
			return s[1 : len(s)-1]
		}
	}
	return s
}

func truncateRunes(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "…"
}

// listSkills merges built-in and installed skills, keyed by directory name.
func listSkills() []skillView {
	byName := map[string]*skillView{}
	var order []string

	for _, root := range builtinSkillDirs() {
		for _, s := range scanSkillDir(root) {
			if _, ok := byName[s.Name]; ok {
				continue
			}
			sv := s
			sv.Builtin = true
			byName[s.Name] = &sv
			order = append(order, s.Name)
		}
	}
	for _, s := range scanSkillDir(agentsSkillsDir()) {
		if existing, ok := byName[s.Name]; ok {
			existing.Installed = true
			existing.InstalledPath = s.Path
			continue
		}
		sv := s
		sv.Installed = true
		sv.InstalledPath = s.Path
		byName[s.Name] = &sv
		order = append(order, s.Name)
	}

	out := make([]skillView, 0, len(order))
	for _, n := range order {
		out = append(out, *byName[n])
	}
	return out
}

// builtinSkill returns the bundled skill with the given name, if any.
func builtinSkill(name string) (string, bool) {
	for _, root := range builtinSkillDirs() {
		dir := filepath.Join(root, name)
		if info, err := os.Stat(dir); err == nil && info.IsDir() {
			if _, err := os.Stat(filepath.Join(dir, skillFile)); err == nil {
				return dir, true
			}
		}
	}
	return "", false
}

/* ---- protocol handlers ---- */

func (s *Server) skillsPayload() map[string]any {
	return map[string]any{"skills": listSkills()}
}

func (s *Server) broadcastSkills() {
	s.broadcast("skills", s.skillsPayload())
}

func (s *Server) handleSkillsList(c *client) {
	s.sendTo(c, "skills", s.skillsPayload())
}

// handleSkillsView returns the SKILL.md of an installed or built-in skill.
func (s *Server) handleSkillsView(c *client, msg message) {
	var p struct {
		Name string `json:"name"`
	}
	if err := json.Unmarshal(msg.Payload, &p); err != nil {
		s.sendError(c, fmt.Errorf("参数错误: %w", err))
		return
	}
	if !validSkillName(p.Name) {
		s.sendError(c, fmt.Errorf("非法技能名: %s", p.Name))
		return
	}
	for _, sv := range listSkills() {
		if sv.Name != p.Name {
			continue
		}
		path := sv.InstalledPath
		if path == "" {
			path = sv.Path
		}
		data, err := os.ReadFile(path)
		if err != nil {
			s.sendError(c, err)
			return
		}
		s.sendTo(c, "skills.content", map[string]any{"name": sv.Name, "content": truncateRunes(string(data), 200000)})
		return
	}
	s.sendError(c, fmt.Errorf("未知技能: %s", p.Name))
}

// handleSkillsInstall links (or copies) a built-in skill into ~/.agents/skills.
func (s *Server) handleSkillsInstall(c *client, msg message) {
	var p struct {
		Name string `json:"name"`
	}
	if err := json.Unmarshal(msg.Payload, &p); err != nil {
		s.sendError(c, fmt.Errorf("参数错误: %w", err))
		return
	}
	if !validSkillName(p.Name) {
		s.sendError(c, fmt.Errorf("非法技能名: %s", p.Name))
		return
	}
	dst := filepath.Join(agentsSkillsDir(), p.Name)
	if _, err := os.Lstat(dst); err == nil {
		s.sendError(c, fmt.Errorf("技能已安装: %s", p.Name))
		return
	}
	src, ok := builtinSkill(p.Name)
	if !ok {
		s.sendError(c, fmt.Errorf("未找到内置技能: %s", p.Name))
		return
	}
	if err := os.MkdirAll(agentsSkillsDir(), 0o755); err != nil {
		s.sendError(c, err)
		return
	}
	if err := os.Symlink(src, dst); err != nil {
		// Some filesystems reject symlinks; fall back to a copy.
		if cerr := copySkillDir(src, dst); cerr != nil {
			s.sendError(c, cerr)
			return
		}
	}
	s.broadcastSkills()
}

// handleSkillsRemove deletes an installed skill from ~/.agents/skills.
func (s *Server) handleSkillsRemove(c *client, msg message) {
	var p struct {
		Name string `json:"name"`
	}
	if err := json.Unmarshal(msg.Payload, &p); err != nil {
		s.sendError(c, fmt.Errorf("参数错误: %w", err))
		return
	}
	if !validSkillName(p.Name) {
		s.sendError(c, fmt.Errorf("非法技能名: %s", p.Name))
		return
	}
	dst := filepath.Join(agentsSkillsDir(), p.Name)
	if _, err := os.Lstat(dst); err != nil {
		s.sendError(c, fmt.Errorf("技能未安装: %s", p.Name))
		return
	}
	if err := os.RemoveAll(dst); err != nil {
		s.sendError(c, err)
		return
	}
	s.broadcastSkills()
}

// copySkillDir copies a skill directory (fallback when symlinks are unavailable).
func copySkillDir(src, dst string) error {
	return filepath.WalkDir(src, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		target := filepath.Join(dst, rel)
		if d.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		in, err := os.Open(path)
		if err != nil {
			return err
		}
		defer in.Close()
		out, err := os.Create(target)
		if err != nil {
			return err
		}
		if _, err := io.Copy(out, in); err != nil {
			out.Close()
			return err
		}
		return out.Close()
	})
}
