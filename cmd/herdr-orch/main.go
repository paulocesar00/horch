// herdr-orch runs the session daemon and installs the horch CLI.
package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/ellingtonsp/herdr-orch/internal/daemon"
	"github.com/ellingtonsp/herdr-orch/internal/paths"
)

func main() {
	if act := os.Getenv("HERDR_PLUGIN_ACTION_ID"); act != "" {
		action(act)
		return
	}
	mode := "daemon"
	if len(os.Args) > 1 {
		mode = os.Args[1]
	}
	switch mode {
	case "daemon":
		runDaemon()
	case "version":
		fmt.Println(daemon.Version)
	case "install-cli":
		dest, err := installCLI()
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		fmt.Println("horch →", dest)
	default:
		fmt.Fprintf(os.Stderr, "usage: herdr-orch [daemon|install-cli|version]\n")
		os.Exit(2)
	}
}

func runDaemon() {
	p, err := paths.Resolve()
	if err != nil {
		log.Fatal(err)
	}
	// Under [[startup]] stdout goes nowhere useful; log to the session's daemon.log.
	if f, err := os.OpenFile(p.Log, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644); err == nil {
		log.SetOutput(f)
	}
	log.SetFlags(log.LstdFlags | log.Lmicroseconds)
	cfg := daemon.DefaultConfig()
	if d, err := time.ParseDuration(os.Getenv("HORCH_IDLE_REPORT_AFTER")); err == nil {
		cfg.IdleReportAfter = d
	}
	if d, err := time.ParseDuration(os.Getenv("HORCH_BLOCKED_AFTER")); err == nil {
		cfg.BlockedAfter = d
	}
	if d, err := time.ParseDuration(os.Getenv("HORCH_UNOBSERVED_AFTER")); err == nil {
		cfg.UnobservedAfter = d
	}
	if exe, err := os.Executable(); err == nil {
		if r, err := filepath.EvalSymlinks(exe); err == nil {
			exe = r
		}
		if h := filepath.Join(filepath.Dir(exe), "horch"); fileExists(h) {
			cfg.HorchBin = h
		}
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM, syscall.SIGHUP)
	defer stop()
	if err := daemon.Run(ctx, daemon.Options{Paths: p, Config: cfg, Logf: log.Printf}); err != nil {
		log.Printf("daemon: %v", err)
		os.Exit(1)
	}
}

func fileExists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}

func herdrBin() string {
	if b := os.Getenv("HERDR_BIN_PATH"); b != "" {
		return b
	}
	return "herdr"
}

// openPane asks herdr to open one of this plugin's panes.

func action(id string) {
	var err error
	switch id {
	case "install-cli":
		var dest string
		dest, err = installCLI()
		if err == nil {
			msg := "horch → " + dest
			if !onPath(filepath.Dir(dest)) {
				msg += " (add " + filepath.Dir(dest) + " to PATH)"
			}
			_ = exec.Command(herdrBin(), "notification", "show", "horch installed", "--body", msg).Run()
			fmt.Println(msg)
		}
	default:
		err = fmt.Errorf("unknown action %q", id)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		_ = exec.Command(herdrBin(), "notification", "show", "horch action failed", "--body", err.Error()).Run()
		os.Exit(1)
	}
}

// targetPane is the pane a split should attach to: the caller's pane, else the focused one.

// installCLI symlinks this plugin's horch into $HORCH_BIN_DIR (default ~/.local/bin). It
// replaces an older symlink but never a regular file it did not create.
func installCLI() (string, error) {
	exe, err := os.Executable()
	if err != nil {
		return "", err
	}
	if r, err := filepath.EvalSymlinks(exe); err == nil {
		exe = r
	}
	src := filepath.Join(filepath.Dir(exe), "horch")
	if !fileExists(src) {
		return "", fmt.Errorf("%s not found; reinstall the plugin", src)
	}
	dir := os.Getenv("HORCH_BIN_DIR")
	if dir == "" {
		home, _ := os.UserHomeDir()
		dir = filepath.Join(home, ".local", "bin")
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	dest := filepath.Join(dir, "horch")
	if fi, err := os.Lstat(dest); err == nil {
		if fi.Mode()&os.ModeSymlink == 0 {
			return "", fmt.Errorf("%s exists and is not a symlink; not replacing it", dest)
		}
		if err := os.Remove(dest); err != nil {
			return "", err
		}
	}
	return dest, os.Symlink(src, dest)
}

func onPath(dir string) bool {
	for _, p := range filepath.SplitList(os.Getenv("PATH")) {
		if filepath.Clean(p) == filepath.Clean(dir) {
			return true
		}
	}
	return false
}
