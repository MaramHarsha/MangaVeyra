package update

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

// Environment variables the update flow reads or sets.
const (
	// EnvCandidate is set for the new binary while it is health-checked on a
	// loopback port: its update module stays idle (no checks, no installs, no
	// recovery) so the candidate never acts on the update that started it.
	EnvCandidate = "MV_UPDATE_CANDIDATE"
	// EnvDeployment=container marks a container install (as does /.dockerenv):
	// G14 takes the backup and shows the host commands instead of swapping.
	EnvDeployment = "MV_DEPLOYMENT"
	// EnvRestart forces the restart method: "exec" (replace the process) or
	// "exit" (exit with RestartExitCode for a supervisor). Default: "exit"
	// under systemd (INVOCATION_ID is set), "exec" otherwise.
	EnvRestart = "MV_RESTART"
)

// RestartExitCode is the exit status after a successful swap under a
// supervisor (EX_TEMPFAIL). The shipped systemd unit restarts on it
// (Restart=on-failure; RestartForceExitStatus=75 makes the intent explicit).
const RestartExitCode = 75

// Candidate limits.
const (
	defaultHealthTimeout = 90 * time.Second
	migrateTimeout       = 30 * time.Minute
	maxChildOutput       = 64 << 10
)

// Launch is how this server process was started; cmd/mangaveyra records it
// so the update flow can run the new binary with the same configuration and
// restart the server after the swap.
type Launch struct {
	// ConfigPath is the --config file ("" when none).
	ConfigPath string
	// Stop ends the serve loop gracefully (cmd/mangaveyra cancels serve's context).
	Stop context.CancelFunc
}

var (
	launchMu      sync.Mutex
	launch        Launch
	restartWanted string // the swapped executable to start after Stop
)

// SetLaunch records how the server was started (cmd/mangaveyra serve).
func SetLaunch(l Launch) {
	launchMu.Lock()
	launch = l
	launchMu.Unlock()
}

func currentLaunch() Launch {
	launchMu.Lock()
	defer launchMu.Unlock()
	return launch
}

// childEnv is the environment of the new binary's migrate and serve: this
// process's environment with the effective data directory and config file
// spelled out, so flags given to serve (--data, --config) reach the child too.
func (m *Module) childEnv(extra ...string) []string {
	env := os.Environ()
	set := func(k, v string) {
		prefix := k + "="
		out := env[:0:0]
		for _, e := range env {
			if !strings.HasPrefix(e, prefix) {
				out = append(out, e)
			}
		}
		env = append(out, prefix+v)
	}
	if dir, err := filepath.Abs(m.d.Cfg.DataDir); err == nil {
		set("MV_DATA_DIR", dir)
	}
	if p := currentLaunch().ConfigPath; p != "" {
		if abs, err := filepath.Abs(p); err == nil {
			set("MV_CONFIG", abs)
		}
	}
	set(EnvRestart, "")
	for _, kv := range extra {
		k, v, _ := strings.Cut(kv, "=")
		set(k, v)
	}
	return env
}

// limitedBuffer keeps the first maxChildOutput bytes of a child's output.
type limitedBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *limitedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if room := maxChildOutput - b.buf.Len(); room > 0 {
		if len(p) > room {
			b.buf.Write(p[:room])
		} else {
			b.buf.Write(p)
		}
	}
	return len(p), nil
}

func (b *limitedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// runChild runs bin with args to completion and returns its combined output.
func (m *Module) runChild(ctx context.Context, timeout time.Duration, bin string, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, bin, args...)
	cmd.Env = m.childEnv(EnvCandidate + "=1")
	cmd.Dir, _ = os.Getwd()
	var out limitedBuffer
	cmd.Stdout, cmd.Stderr = &out, &out
	cmd.WaitDelay = 5 * time.Second
	m.trackChild(cmd)
	err := cmd.Run()
	m.untrackChild(cmd)
	if ctx.Err() == context.DeadlineExceeded {
		err = fmt.Errorf("%s %s timed out after %s", filepath.Base(bin), strings.Join(args, " "), timeout)
	}
	return strings.TrimSpace(out.String()), err
}

func (m *Module) trackChild(c *exec.Cmd) {
	m.childMu.Lock()
	m.children[c] = struct{}{}
	m.childMu.Unlock()
}

func (m *Module) untrackChild(c *exec.Cmd) {
	m.childMu.Lock()
	delete(m.children, c)
	m.childMu.Unlock()
}

// killChildren stops every child still running (server shutdown).
func (m *Module) killChildren() {
	m.childMu.Lock()
	defer m.childMu.Unlock()
	for c := range m.children {
		if c.Process != nil {
			_ = c.Process.Kill()
		}
	}
}

var (
	pendingLine = regexp.MustCompile(`(?m)\spending\s*$`)
	appliedLine = regexp.MustCompile(`applied (\d+) migration`)
	migrateFile = regexp.MustCompile(`migrate (\d{4})_`)
)

// countPending counts the "pending" rows of `migrate status`.
func countPending(out string) int { return len(pendingLine.FindAllString(out, -1)) }

// countApplied reads "applied N migration(s)" from `migrate` (0 when up to date).
func countApplied(out string) int {
	if mm := appliedLine.FindStringSubmatch(out); mm != nil {
		n, _ := strconv.Atoi(mm[1])
		return n
	}
	return 0
}

// migrationNumber extracts the failing migration's number from `migrate` output.
func migrationNumber(out string) string {
	if mm := migrateFile.FindStringSubmatch(out); mm != nil {
		return mm[1]
	}
	return ""
}

// freeLoopbackAddr picks an unused loopback port for the candidate.
func freeLoopbackAddr() (string, error) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return "", err
	}
	addr := ln.Addr().String()
	_ = ln.Close()
	return addr, nil
}

// healthResult is what the candidate reported.
type healthResult struct {
	WorkersOnline int
}

// checkCandidate starts `<bin> serve` on a loopback port with this server's
// configuration, waits until /healthz reports version want and /readyz is
// ready, and stops it again.
func (m *Module) checkCandidate(ctx context.Context, bin, want string, logf func(string)) (healthResult, error) {
	var res healthResult
	addr, err := freeLoopbackAddr()
	if err != nil {
		return res, err
	}
	args := []string{"serve", "--addr", addr}
	if m.d.Cfg.Dev {
		args = append(args, "--dev")
	}
	cmd := exec.Command(bin, args...)
	cmd.Env = m.childEnv(EnvCandidate+"=1", "MV_ADDR="+addr)
	cmd.Dir, _ = os.Getwd()
	var out limitedBuffer
	cmd.Stdout, cmd.Stderr = &out, &out
	if err := cmd.Start(); err != nil {
		return res, err
	}
	m.trackChild(cmd)
	exited := make(chan error, 1)
	go func() { exited <- cmd.Wait() }()
	defer func() {
		stopChild(cmd, exited, m.d.Cfg.ShutdownTimeout+5*time.Second)
		m.untrackChild(cmd)
		if s := strings.TrimSpace(out.String()); s != "" {
			logf("new version output:\n" + lastLines(s, 40))
		}
	}()
	timeout := m.hooks.HealthTimeout
	if timeout <= 0 {
		timeout = defaultHealthTimeout
	}
	deadline := time.NewTimer(timeout)
	defer deadline.Stop()
	tick := time.NewTicker(200 * time.Millisecond)
	defer tick.Stop()
	client := &http.Client{Timeout: 3 * time.Second, Transport: &http.Transport{Proxy: nil}}
	base := "http://" + addr
	var last error
	for {
		select {
		case <-ctx.Done():
			return res, ctx.Err()
		case err := <-exited:
			exited <- err // let the deferred stop see it
			return res, fmt.Errorf("the new version stopped before passing its health check: %v", err)
		case <-deadline.C:
			if last == nil {
				last = errors.New("no answer")
			}
			return res, fmt.Errorf("the new version did not pass its health check within %s: %v", timeout, last)
		case <-tick.C:
			var hz struct {
				Status  string `json:"status"`
				Version string `json:"version"`
			}
			if code, err := getJSON(ctx, client, base+"/healthz", &hz); err != nil || code != http.StatusOK || hz.Status != "ok" {
				last = errOr(err, fmt.Errorf("/healthz answered %d", code))
				continue
			}
			if !sameVersion(hz.Version, want) {
				return res, fmt.Errorf("the new version reports %q, not %q", hz.Version, want)
			}
			var rz struct {
				Ready  bool `json:"ready"`
				Checks map[string]struct {
					OK     bool            `json:"ok"`
					Error  string          `json:"error"`
					Detail json.RawMessage `json:"detail"`
				} `json:"checks"`
			}
			code, err := getJSON(ctx, client, base+"/readyz", &rz)
			if err != nil || code != http.StatusOK || !rz.Ready {
				last = errOr(err, fmt.Errorf("/readyz answered %d", code))
				for name, c := range rz.Checks {
					if !c.OK && c.Error != "" {
						last = fmt.Errorf("%s: %s", name, c.Error)
					}
				}
				continue
			}
			if j, ok := rz.Checks["jobs"]; ok {
				var d struct {
					WorkersOnline int `json:"workers_online"`
				}
				_ = json.Unmarshal(j.Detail, &d)
				res.WorkersOnline = d.WorkersOnline
			}
			return res, nil
		}
	}
}

func errOr(err, def error) error {
	if err != nil {
		return err
	}
	return def
}

func getJSON(ctx context.Context, c *http.Client, url string, v any) (int, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return 0, err
	}
	resp, err := c.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	err = json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(v)
	return resp.StatusCode, err
}

// stopChild asks the child to shut down gracefully and kills it after wait.
func stopChild(cmd *exec.Cmd, exited chan error, wait time.Duration) {
	if cmd.Process == nil {
		return
	}
	select {
	case err := <-exited:
		exited <- err
		return
	default:
	}
	if err := cmd.Process.Signal(syscall.SIGTERM); err != nil {
		_ = cmd.Process.Kill()
	}
	select {
	case <-exited:
	case <-time.After(wait):
		_ = cmd.Process.Kill()
		<-exited
	}
}

func lastLines(s string, n int) string {
	lines := strings.Split(s, "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.Join(lines, "\n")
}

var secretURL = regexp.MustCompile(`([a-z][a-z0-9+.-]*://)[^/\s:@]+:[^@\s]+@`)

// redact removes credentials from URLs in log text (database URLs).
func redact(s string) string { return secretURL.ReplaceAllString(s, "${1}…@") }
