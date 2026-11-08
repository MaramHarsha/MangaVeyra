package update

import (
	"fmt"
	"os"
)

// RestartPending reports whether an update swapped the executable and asked
// the serve loop to stop so the new binary can start.
func RestartPending() bool {
	launchMu.Lock()
	defer launchMu.Unlock()
	return restartWanted != ""
}

// RestartIfPending starts the swapped executable after serve returned for an
// update (cmd/mangaveyra calls it once the server has shut down). Under a
// supervisor (systemd sets INVOCATION_ID, or MV_RESTART=exit) the process
// exits with RestartExitCode and the supervisor starts the new binary;
// otherwise the process replaces itself with the new binary, keeping its PID,
// arguments and environment (MV_RESTART=exec). It returns nil when no restart
// is pending.
func RestartIfPending() error {
	launchMu.Lock()
	exe := restartWanted
	launchMu.Unlock()
	if exe == "" {
		return nil
	}
	if restartMethod(os.Getenv) == "exit" {
		fmt.Fprintf(os.Stderr, "update installed; exiting with status %d so the service manager starts %s\n", RestartExitCode, exe)
		os.Exit(RestartExitCode)
	}
	fmt.Fprintf(os.Stderr, "update installed; restarting as %s\n", exe)
	return execSelf(exe)
}

// restartMethod is "exit" under a supervisor and "exec" otherwise.
func restartMethod(getenv func(string) string) string {
	switch getenv(EnvRestart) {
	case "exec":
		return "exec"
	case "exit":
		return "exit"
	}
	if getenv("INVOCATION_ID") != "" {
		return "exit"
	}
	return "exec"
}

// requestRestart records exe and stops the serve loop.
func (m *Module) requestRestart(exe string) {
	if m.hooks.Restart != nil {
		m.hooks.Restart(exe)
		return
	}
	l := currentLaunch()
	if l.Stop == nil {
		m.d.Log.Warn("update: installed; restart the server to finish the update", "executable", exe)
		return
	}
	launchMu.Lock()
	restartWanted = exe
	launchMu.Unlock()
	l.Stop()
}
