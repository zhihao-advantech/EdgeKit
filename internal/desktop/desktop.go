// Package desktop manages a remote graphical session (VNC) on a Linux board.
//
// The board's VNC server is expected to listen on loopback only; EdgeKit reaches
// it through the existing SSH connection (sshclient.Manager.Dial), so no extra
// credentials or exposed ports are needed. The VNC server is started on demand
// over SSH (x11vnc for the X server ecosystem).
package desktop

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// DefaultPort is the RFB port for display :0 (5900 + 0).
const DefaultPort = 5900

// Execer is the slice of the SSH capability the desktop manager needs. It is
// satisfied by *sshclient.Manager.
type Execer interface {
	IsConnected() bool
	ExecCapture(command string, maxBytes int) (string, error)
	ExecCaptureTimeout(command string, maxBytes int, timeout time.Duration) (string, error)
}

// Session describes a live remote desktop: the SSH connection it borrows and
// the loopback RFB port on the board.
type Session struct {
	SSH     Execer
	Display string
	Port    int
}

// EnsureVNC makes sure an x11vnc server is running on the board's display and
// returns the loopback port it listens on. It is best-effort: the exact X auth
// is guessed (-auth guess) and failures are reported back to the caller.
func EnsureVNC(ctx context.Context, ssh Execer, display string, port int) (int, error) {
	if ssh == nil || !ssh.IsConnected() {
		return 0, fmt.Errorf("SSH 未连接")
	}
	if display == "" {
		display = ":0"
	}
	if port == 0 {
		port = DefaultPort
	}
	if !strings.HasPrefix(display, ":") {
		display = ":" + display
	}

	// Install x11vnc on demand when the board lacks it, then start it if it is
	// not already serving this port, and finally report status.
	script := fmt.Sprintf(`SUDO=""
if [ "$(id -u)" != "0" ] && command -v sudo >/dev/null 2>&1; then SUDO="sudo -n"; fi
if ! command -v x11vnc >/dev/null 2>&1; then
  if command -v apt-get >/dev/null 2>&1; then
    $SUDO apt-get update -qq >/dev/null 2>&1; $SUDO apt-get install -y -qq x11vnc >/dev/null 2>&1
  elif command -v dnf >/dev/null 2>&1; then $SUDO dnf install -y x11vnc >/dev/null 2>&1
  elif command -v yum >/dev/null 2>&1; then $SUDO yum install -y x11vnc >/dev/null 2>&1
  elif command -v opkg >/dev/null 2>&1; then $SUDO opkg update >/dev/null 2>&1; $SUDO opkg install x11vnc >/dev/null 2>&1
  fi
fi
if command -v x11vnc >/dev/null 2>&1; then
  if ! pgrep -f 'x11vnc.*-rfbport %[2]d' >/dev/null 2>&1; then
    DISPLAY=%[1]s x11vnc -display %[1]s -auth guess -localhost -nopw -forever -shared -rfbport %[2]d -bg -o /tmp/edgekit-x11vnc.log >/dev/null 2>&1 || true
    sleep 1
  fi
  echo EDGEKIT_VNC_OK
else
  echo EDGEKIT_VNC_MISSING
fi`, display, port)

	// Installing a package can take a while; allow up to 3 minutes.
	out, err := ssh.ExecCaptureTimeout(script, 8*1024, 180*time.Second)
	if err != nil {
		return 0, fmt.Errorf("准备 x11vnc 失败: %w", err)
	}
	switch {
	case strings.Contains(out, "EDGEKIT_VNC_OK"):
		return port, nil
	case strings.Contains(out, "EDGEKIT_VNC_MISSING"):
		return 0, fmt.Errorf("板子上未能安装 x11vnc（请检查网络或手动 apt install x11vnc）")
	default:
		return 0, fmt.Errorf("无法确认 x11vnc 状态: %s", strings.TrimSpace(out))
	}
}

// StopVNC stops the x11vnc instance serving the given port (best effort). It is
// only called when EdgeKit started the server for this session.
func StopVNC(ctx context.Context, ssh Execer, port int) {
	if ssh == nil || !ssh.IsConnected() {
		return
	}
	cmd := "pkill -f " + shellQuote("x11vnc.*-rfbport "+strconv.Itoa(port)) + " >/dev/null 2>&1 || true"
	_, _ = ssh.ExecCapture(cmd, 1024)
}

func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}
