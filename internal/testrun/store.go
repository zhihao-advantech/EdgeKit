package testrun

import (
	"encoding/json"
	"fmt"
	"path"
	"sort"
	"strings"
	"time"

	"edgekit/internal/workspace"
)

// Store archives finished runs into the local workspace under
// tests/runs/<id>/ so they can be reviewed or re-run later, and keeps the
// reusable test definitions under tests/<name>.test.json.
type Store struct{}

// DefinitionRef is a saved test case in the workspace.
type DefinitionRef struct {
	Path string `json:"path"`
	Name string `json:"name"`
}

// RunSummary describes one archived run.
type RunSummary struct {
	ID        string    `json:"id"`
	Name      string    `json:"name"`
	Status    string    `json:"status"`
	CreatedAt time.Time `json:"createdAt"`
	Path      string    `json:"path"`
}

// Archive writes the run bundle (run.json, definition.json, report.md) and
// returns the workspace-relative directory.
func (Store) Archive(run *Run) (string, error) {
	rel := path.Join("tests", "runs", run.ID)

	runJSON, err := json.MarshalIndent(run, "", "  ")
	if err != nil {
		return "", err
	}
	if _, err := workspace.Write(rel+"/run.json", runJSON); err != nil {
		return "", err
	}

	defJSON, err := json.MarshalIndent(run.Definition, "", "  ")
	if err != nil {
		return "", err
	}
	if _, err := workspace.Write(rel+"/definition.json", defJSON); err != nil {
		return "", err
	}

	if _, err := workspace.Write(rel+"/report.md", []byte(run.PhaseOutput(PhaseGenerate))); err != nil {
		return "", err
	}
	return rel, nil
}

// ListDefinitions returns the saved test cases (tests/*.test.json).
func (Store) ListDefinitions() []DefinitionRef {
	entries, err := workspace.List("tests")
	if err != nil {
		return nil
	}
	out := make([]DefinitionRef, 0, len(entries))
	for _, e := range entries {
		if e.IsDir || !strings.HasSuffix(e.Name, ".test.json") {
			continue
		}
		rel := path.Join("tests", e.Name)
		name := strings.TrimSuffix(e.Name, ".test.json")
		var def Definition
		if data, err := workspace.Read(rel); err == nil {
			if json.Unmarshal(data, &def) == nil && def.Name != "" {
				name = def.Name
			}
		}
		out = append(out, DefinitionRef{Path: rel, Name: name})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// ReadDefinition loads a saved test case.
func (Store) ReadDefinition(rel string) (Definition, error) {
	rel = path.Clean(rel)
	if !strings.HasPrefix(rel, "tests/") || !strings.HasSuffix(rel, ".test.json") {
		return Definition{}, fmt.Errorf("不是有效的测试定义路径: %s", rel)
	}
	data, err := workspace.Read(rel)
	if err != nil {
		return Definition{}, err
	}
	var def Definition
	if err := json.Unmarshal(data, &def); err != nil {
		return Definition{}, fmt.Errorf("解析测试定义失败: %w", err)
	}
	return def, nil
}

// WriteDefinition saves a test case to tests/<name>.test.json and returns the
// workspace-relative path.
func (Store) WriteDefinition(def Definition) (string, error) {
	name := def.Name
	if strings.TrimSpace(name) == "" {
		name = "test"
		def.Name = name
	}
	rel := path.Join("tests", safeName(name)+".test.json")
	data, err := json.MarshalIndent(def, "", "  ")
	if err != nil {
		return "", err
	}
	if _, err := workspace.Write(rel, data); err != nil {
		return "", err
	}
	return rel, nil
}

// ListRuns returns the archived runs, newest first.
func (Store) ListRuns() []RunSummary {
	entries, err := workspace.List("tests/runs")
	if err != nil {
		return nil
	}
	out := make([]RunSummary, 0, len(entries))
	for _, e := range entries {
		if !e.IsDir {
			continue
		}
		rel := path.Join("tests", "runs", e.Name)
		data, err := workspace.Read(rel + "/run.json")
		if err != nil {
			continue
		}
		var run Run
		if json.Unmarshal(data, &run) != nil {
			continue
		}
		out = append(out, RunSummary{ID: run.ID, Name: run.Name, Status: run.Status, CreatedAt: run.CreatedAt, Path: rel})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.After(out[j].CreatedAt) })
	return out
}

// ReadRun loads an archived run.
func (Store) ReadRun(id string) (Run, error) {
	if id == "" || strings.ContainsAny(id, `/\`) {
		return Run{}, fmt.Errorf("无效的运行 ID: %q", id)
	}
	data, err := workspace.Read(path.Join("tests", "runs", id, "run.json"))
	if err != nil {
		return Run{}, err
	}
	var run Run
	if err := json.Unmarshal(data, &run); err != nil {
		return Run{}, fmt.Errorf("解析运行记录失败: %w", err)
	}
	return run, nil
}

// Previous returns the most recent archived run with the same name other than
// excludeID, for the report's regression comparison.
func (s Store) Previous(name, excludeID string) (Run, bool) {
	if strings.TrimSpace(name) == "" {
		return Run{}, false
	}
	for _, summary := range s.ListRuns() { // newest first
		if summary.ID == excludeID || summary.Name != name {
			continue
		}
		run, err := s.ReadRun(summary.ID)
		if err == nil {
			return run, true
		}
	}
	return Run{}, false
}

// safeName turns a test name into a filename component.
func safeName(s string) string {
	s = strings.TrimSpace(s)
	s = strings.ReplaceAll(s, "/", "_")
	s = strings.ReplaceAll(s, `\`, "_")
	s = strings.ReplaceAll(s, "..", "_")
	if s == "" {
		s = "test"
	}
	return s
}
