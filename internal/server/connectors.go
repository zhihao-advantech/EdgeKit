package server

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"edgekit/internal/kit"
	"edgekit/internal/kits"
	"edgekit/internal/mcpclient"
)

// connectorConfig is one external MCP server the user wants to attach (a
// knowledge base / RAG service, a filesystem server, ...).
type connectorConfig struct {
	ID      string            `json:"id"`
	Name    string            `json:"name,omitempty"`
	Command string            `json:"command"`
	Args    []string          `json:"args,omitempty"`
	Env     map[string]string `json:"env,omitempty"`
	Enabled bool              `json:"enabled"`
	Risk    string            `json:"risk,omitempty"` // "read" | "mutate" (default)
}

type connectorsFile struct {
	Servers []connectorConfig `json:"servers"`
}

// connectorsPath returns the connectors config file next to settings.json.
func connectorsPath() string {
	dir, err := os.UserConfigDir()
	if err != nil {
		dir = os.TempDir()
	}
	dir = filepath.Join(dir, "edgekit")
	_ = os.MkdirAll(dir, 0o755)
	return filepath.Join(dir, "connectors.json")
}

func loadConnectors() []connectorConfig {
	data, err := os.ReadFile(connectorsPath())
	if err != nil {
		return nil
	}
	var f connectorsFile
	if json.Unmarshal(data, &f) != nil {
		return nil
	}
	return f.Servers
}

func saveConnectors(list []connectorConfig) error {
	data, err := json.MarshalIndent(connectorsFile{Servers: list}, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(connectorsPath(), data, 0o600)
}

// connectorView is one connector's state for the UI.
type connectorView struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Command   string `json:"command"`
	Enabled   bool   `json:"enabled"`
	Connected bool   `json:"connected"`
	Tools     int    `json:"tools"`
	Error     string `json:"error,omitempty"`
}

type connectorState struct {
	cfg        connectorConfig
	client     *mcpclient.Client
	connected  bool
	err        string
	toolCount  int
	generation int
}

// connectorManager owns the external MCP child processes and mirrors them into
// the kit Registry.
type connectorManager struct {
	s  *Server
	mu sync.Mutex
	m  map[string]*connectorState
}

func newConnectorManager(s *Server) *connectorManager {
	return &connectorManager{s: s, m: make(map[string]*connectorState)}
}

// start loads the config and connects every enabled connector in the
// background so a broken one never blocks startup.
func (m *connectorManager) start() {
	for _, cfg := range loadConnectors() {
		m.mu.Lock()
		m.m[cfg.ID] = &connectorState{cfg: cfg}
		m.mu.Unlock()
	}
	for _, cfg := range loadConnectors() {
		if cfg.Enabled && strings.TrimSpace(cfg.Command) != "" {
			go m.connect(cfg.ID)
		}
	}
}

// connect (re)spawns a connector and registers its tools.
func (m *connectorManager) connect(id string) {
	m.mu.Lock()
	st := m.m[id]
	if st == nil {
		m.mu.Unlock()
		return
	}
	cfg := st.cfg
	st.generation++
	gen := st.generation
	if st.client != nil {
		_ = st.client.Close()
		st.client = nil
	}
	m.mu.Unlock()

	cli := mcpclient.New(mcpclient.Options{
		ID:      cfg.ID,
		Name:    cfg.Name,
		Command: cfg.Command,
		Args:    cfg.Args,
		Env:     cfg.Env,
		Logf: func(format string, args ...any) {
			log.Printf("connector %s: "+format, append([]any{cfg.ID}, args...)...)
		},
	})

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	if err := cli.Start(ctx); err != nil {
		m.mu.Lock()
		if m.m[id] == st {
			st.err = err.Error()
			st.connected = false
			st.toolCount = 0
		}
		m.mu.Unlock()
		m.s.kits.Remove("edgekit.connector." + id)
		m.broadcastConnectors()
		return
	}

	risk := kit.RiskMutate
	if strings.EqualFold(cfg.Risk, "read") {
		risk = kit.RiskRead
	}
	display := cfg.Name
	if display == "" {
		display = cfg.ID
	}
	m.s.kits.Replace(kits.NewMCPKit(cfg.ID, display, cli, risk))

	m.mu.Lock()
	if m.m[id] != st || st.generation != gen {
		// superseded by a newer connect/disconnect
		m.mu.Unlock()
		_ = cli.Close()
		return
	}
	st.client = cli
	st.connected = true
	st.err = ""
	st.toolCount = len(cli.Tools())
	m.mu.Unlock()

	m.s.broadcast("kits", m.s.kitsPayload())
	m.broadcastConnectors()
	go m.supervise(id, st, gen, cli)
}

// supervise clears the connector's tools when its process exits.
func (m *connectorManager) supervise(id string, st *connectorState, gen int, cli *mcpclient.Client) {
	<-cli.Done()
	m.mu.Lock()
	current := m.m[id] == st && st.generation == gen && st.client == cli
	if current {
		st.connected = false
		st.toolCount = 0
		st.err = "连接已断开"
	}
	m.mu.Unlock()
	if current {
		m.s.kits.Remove("edgekit.connector." + id)
		m.s.broadcast("kits", m.s.kitsPayload())
		m.broadcastConnectors()
	}
}

// disconnect stops a connector (if running) and unregisters its tools.
func (m *connectorManager) disconnect(id string) {
	m.mu.Lock()
	st := m.m[id]
	if st == nil {
		m.mu.Unlock()
		return
	}
	st.generation++
	cli := st.client
	st.client = nil
	st.connected = false
	st.toolCount = 0
	st.err = ""
	m.mu.Unlock()

	if cli != nil {
		_ = cli.Close()
	}
	m.s.kits.Remove("edgekit.connector." + id)
	m.s.broadcast("kits", m.s.kitsPayload())
	m.broadcastConnectors()
}

// shutdown stops every connector (called on server Close).
func (m *connectorManager) shutdown() {
	m.mu.Lock()
	clients := make([]*mcpclient.Client, 0, len(m.m))
	for _, st := range m.m {
		if st.client != nil {
			clients = append(clients, st.client)
			st.client = nil
		}
	}
	m.mu.Unlock()
	for _, c := range clients {
		_ = c.Close()
	}
}

func (m *connectorManager) views() []connectorView {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]connectorView, 0, len(m.m))
	for _, cfg := range loadConnectors() {
		st := m.m[cfg.ID]
		v := connectorView{ID: cfg.ID, Name: cfg.Name, Command: cfg.Command, Enabled: cfg.Enabled}
		if v.Name == "" {
			v.Name = cfg.ID
		}
		if st != nil {
			v.Connected = st.connected
			v.Tools = st.toolCount
			v.Error = st.err
		}
		out = append(out, v)
	}
	return out
}

func (m *connectorManager) broadcastConnectors() {
	m.s.broadcast("connectors", map[string]any{"connectors": m.views()})
}

/* ---- protocol handlers ---- */

func (m *connectorManager) handleList(c *client) {
	m.s.sendTo(c, "connectors", map[string]any{"connectors": m.views()})
}

// handleSetEnabled persists the connector's enabled flag and connects or
// disconnects it.
func (m *connectorManager) handleSetEnabled(c *client, msg message) {
	var p struct {
		ID      string `json:"id"`
		Enabled bool   `json:"enabled"`
	}
	if err := json.Unmarshal(msg.Payload, &p); err != nil {
		m.s.sendError(c, fmt.Errorf("参数错误: %w", err))
		return
	}
	list := loadConnectors()
	found := false
	for i := range list {
		if list[i].ID == p.ID {
			list[i].Enabled = p.Enabled
			found = true
		}
	}
	if !found {
		m.s.sendError(c, fmt.Errorf("未知连接器: %s", p.ID))
		return
	}
	if err := saveConnectors(list); err != nil {
		m.s.sendError(c, err)
		return
	}
	m.mu.Lock()
	if st := m.m[p.ID]; st != nil {
		st.cfg.Enabled = p.Enabled
	} else {
		for _, cfg := range list {
			if cfg.ID == p.ID {
				m.m[p.ID] = &connectorState{cfg: cfg}
			}
		}
	}
	m.mu.Unlock()

	if p.Enabled {
		go m.connect(p.ID)
	} else {
		m.disconnect(p.ID)
	}
}

func (m *connectorManager) handleReconnect(c *client, msg message) {
	var p struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(msg.Payload, &p); err != nil {
		m.s.sendError(c, fmt.Errorf("参数错误: %w", err))
		return
	}
	if _, ok := m.m[p.ID]; !ok {
		// load from disk in case it was added to the file while running
		for _, cfg := range loadConnectors() {
			if cfg.ID == p.ID {
				m.mu.Lock()
				m.m[cfg.ID] = &connectorState{cfg: cfg}
				m.mu.Unlock()
			}
		}
	}
	go m.connect(p.ID)
}
