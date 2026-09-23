// Package serial provides a thin, concurrency-safe wrapper around a physical
// or virtual serial port. It exposes enumeration, open/close and a read loop
// that reports every chunk of traffic through a callback.
package serial

import (
	"fmt"
	"strings"
	"sync"
	"time"

	"go.bug.st/serial"
	"go.bug.st/serial/enumerator"
)

// Config describes how a port should be opened.
type Config struct {
	Port     string  `json:"port"`
	Baud     int     `json:"baud"`
	DataBits int     `json:"dataBits"`
	Parity   string  `json:"parity"`
	StopBits float64 `json:"stopBits"`
}

// PortInfo is a single enumerated serial port.
type PortInfo struct {
	Name         string `json:"name"`
	Description  string `json:"description"`
	Manufacturer string `json:"manufacturer"`
	VID          string `json:"vid"`
	PID          string `json:"pid"`
	SerialNumber string `json:"serialNumber"`
	IsUSB        bool   `json:"isUSB"`
}

// Direction values used by Event.
const (
	DirRX    = "rx"
	DirTX    = "tx"
	DirInfo  = "info"
	DirError = "error"
)

// Event is a chunk of traffic or a lifecycle notification.
type Event struct {
	Direction string    `json:"direction"`
	Data      []byte    `json:"data"`
	Time      time.Time `json:"time"`
}

// recentMax bounds the receive history kept for the agent to inspect.
const recentMax = 64 * 1024

// Manager owns at most one open port at a time.
type Manager struct {
	mu        sync.Mutex
	port      serial.Port
	cfg       Config
	open      bool
	done      chan struct{}
	recent    []byte
	capturing bool
	onEvent   func(Event)
}

// New creates a manager that reports traffic through onEvent. onEvent must be
// safe for concurrent use; it is invoked from the read loop goroutine.
func New(onEvent func(Event)) *Manager {
	return &Manager{onEvent: onEvent}
}

// List enumerates the serial ports currently present on the system.
func (m *Manager) List() ([]PortInfo, error) {
	ports, err := enumerator.GetDetailedPortsList()
	if err != nil {
		return nil, fmt.Errorf("enumerate serial ports: %w", err)
	}
	out := make([]PortInfo, 0, len(ports))
	for _, p := range ports {
		out = append(out, PortInfo{
			Name:         p.Name,
			Description:  p.Product,
			VID:          p.VID,
			PID:          p.PID,
			SerialNumber: p.SerialNumber,
			IsUSB:        p.IsUSB,
		})
	}
	return out, nil
}

// Open opens cfg.Port and starts the background reader.
func (m *Manager) Open(cfg Config) error {
	if cfg.Port == "" {
		return fmt.Errorf("no serial port selected")
	}
	if cfg.Baud <= 0 {
		cfg.Baud = 115200
	}
	if cfg.DataBits == 0 {
		cfg.DataBits = 8
	}
	if cfg.StopBits == 0 {
		cfg.StopBits = 1
	}

	m.mu.Lock()
	if m.open {
		m.mu.Unlock()
		return fmt.Errorf("serial port %s is already open", m.cfg.Port)
	}
	mode := &serial.Mode{
		BaudRate: cfg.Baud,
		DataBits: cfg.DataBits,
		Parity:   parseParity(cfg.Parity),
		StopBits: parseStopBits(cfg.StopBits),
	}
	port, err := serial.Open(cfg.Port, mode)
	if err != nil {
		m.mu.Unlock()
		return fmt.Errorf("open %s: %w", cfg.Port, err)
	}
	// A read timeout keeps the read loop responsive to Close.
	_ = port.SetReadTimeout(200 * time.Millisecond)

	m.port = port
	m.cfg = cfg
	m.open = true
	m.recent = nil
	m.done = make(chan struct{})
	done := m.done
	m.mu.Unlock()

	m.emit(Event{
		Direction: DirInfo,
		Data:      []byte(fmt.Sprintf("已打开 %s @ %d %d%s%d", cfg.Port, cfg.Baud, cfg.DataBits, parityLabel(cfg.Parity), int(cfg.StopBits))),
		Time:      time.Now(),
	})
	go m.readLoop(port, done)
	return nil
}

// Close stops the reader and closes the port. It is safe to call when closed.
func (m *Manager) Close() error {
	m.mu.Lock()
	if !m.open {
		m.mu.Unlock()
		return nil
	}
	port := m.port
	closed := m.cfg.Port
	close(m.done)
	m.port = nil
	m.open = false
	m.mu.Unlock()

	err := port.Close()
	m.emit(Event{Direction: DirInfo, Data: []byte("已关闭 " + closed), Time: time.Now()})
	if err != nil {
		return fmt.Errorf("close %s: %w", closed, err)
	}
	return nil
}

// Write sends data to the port and reports it as a TX event.
func (m *Manager) Write(data []byte) error {
	m.mu.Lock()
	if !m.open {
		m.mu.Unlock()
		return fmt.Errorf("serial port is not open")
	}
	_, err := m.port.Write(data)
	m.mu.Unlock()
	if err != nil {
		return fmt.Errorf("write: %w", err)
	}
	m.emit(Event{Direction: DirTX, Data: data, Time: time.Now()})
	return nil
}

// Port returns the name of the open port (empty when closed).
func (m *Manager) Port() string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.cfg.Port
}

// Recent returns a copy of the most recently received bytes.
func (m *Manager) Recent() []byte {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]byte, len(m.recent))
	copy(out, m.recent)
	return out
}

func (m *Manager) isCapturing() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.capturing
}

func (m *Manager) setCapturing(v bool) {
	m.mu.Lock()
	m.capturing = v
	m.mu.Unlock()
}

// writeRaw writes to the port without emitting a TX event.
func (m *Manager) writeRaw(p []byte) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if !m.open {
		return fmt.Errorf("serial port is not open")
	}
	_, err := m.port.Write(p)
	return err
}

func (m *Manager) appendRecent(p []byte) {
	m.mu.Lock()
	m.recent = append(m.recent, p...)
	if len(m.recent) > recentMax {
		n := copy(m.recent, m.recent[len(m.recent)-recentMax:])
		m.recent = m.recent[:n]
	}
	m.mu.Unlock()
}

// IsOpen reports whether a port is currently open.
func (m *Manager) IsOpen() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.open
}

// Config returns the configuration of the open port (zero value when closed).
func (m *Manager) Config() Config {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.cfg
}

func (m *Manager) readLoop(port serial.Port, done chan struct{}) {
	buf := make([]byte, 4096)
	for {
		select {
		case <-done:
			return
		default:
		}
		n, err := port.Read(buf)
		if err != nil {
			select {
			case <-done:
				// Close() initiated the teardown; no need to report.
			default:
				m.emit(Event{Direction: DirError, Data: []byte("读取错误: " + err.Error()), Time: time.Now()})
				_ = m.Close()
			}
			return
		}
		if n > 0 {
			chunk := make([]byte, n)
			copy(chunk, buf[:n])
			m.appendRecent(chunk)
			// While a silent capture is running, keep the captured traffic out
			// of the terminal stream (it is still recorded in `recent`).
			if !m.isCapturing() {
				m.emit(Event{Direction: DirRX, Data: chunk, Time: time.Now()})
			}
		}
	}
}

func (m *Manager) emit(ev Event) {
	if m.onEvent != nil {
		m.onEvent(ev)
	}
}

func parseParity(s string) serial.Parity {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "odd":
		return serial.OddParity
	case "even":
		return serial.EvenParity
	case "mark":
		return serial.MarkParity
	case "space":
		return serial.SpaceParity
	default:
		return serial.NoParity
	}
}

func parseStopBits(v float64) serial.StopBits {
	switch v {
	case 1.5:
		return serial.OnePointFiveStopBits
	case 2:
		return serial.TwoStopBits
	default:
		return serial.OneStopBit
	}
}

func parityLabel(s string) string {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "odd":
		return "O"
	case "even":
		return "E"
	case "mark":
		return "M"
	case "space":
		return "S"
	default:
		return "N"
	}
}
