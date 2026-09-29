package testrun

import (
	"encoding/json"
	"path"

	"edgekit/internal/workspace"
)

// Store archives finished runs into the local workspace under
// tests/runs/<id>/ so they can be reviewed or re-run later.
type Store struct{}

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
