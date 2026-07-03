package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"
)

// Set by GoReleaser via ldflags.
var (
	version = "dev"
	commit  = "none"
	date    = "unknown"
)

const (
	releaseAPI  = "https://api.github.com/repos/hackclub/hcb-cli-and-mcp/releases/latest"
	brewFormula = "hackclub/hcb/hcb" // tap: brew tap hackclub/hcb https://github.com/hackclub/hcb-cli-and-mcp
)

func versionCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "version",
		Short: "Print the hcb version",
		Run: func(cmd *cobra.Command, args []string) {
			fmt.Printf("hcb %s (commit %s, built %s)\n", version, commit, date)
		},
	}
}

func upgradeCmd() *cobra.Command {
	var background bool
	cmd := &cobra.Command{
		Use:   "upgrade",
		Short: "Update hcb to the latest release via Homebrew",
		RunE: func(cmd *cobra.Command, args []string) error {
			if background {
				runBackgroundUpdate()
				return nil
			}
			exe, _ := os.Executable()
			if !brewManaged(resolvePath(exe)) {
				fmt.Fprintln(os.Stderr, "hcb was not installed with Homebrew, so it can't self-update.")
				fmt.Fprintln(os.Stderr, "Install via brew to enable updates:")
				fmt.Fprintln(os.Stderr, "  brew tap hackclub/hcb https://github.com/hackclub/hcb-cli-and-mcp")
				fmt.Fprintln(os.Stderr, "  brew install hcb")
				return nil
			}
			latest, err := fetchLatestVersion(releaseAPI)
			if err == nil && !newerVersion(version, latest) {
				fmt.Fprintf(os.Stderr, "hcb %s is already the latest version.\n", version)
				return nil
			}
			up := exec.Command("brew", "upgrade", brewFormula)
			up.Stdout, up.Stderr = os.Stdout, os.Stderr
			return up.Run()
		},
	}
	cmd.Flags().BoolVar(&background, "background", false, "run the throttled background update check")
	cmd.Flags().MarkHidden("background")
	return cmd
}

// --- automatic background updates ---

type updateState struct {
	CheckedAt int64  `json:"checked_at"`
	Notice    string `json:"notice,omitempty"`
}

func updateStatePath() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".config", "hcb", "update_state.json")
}

func readUpdateState(path string) updateState {
	var st updateState
	if b, err := os.ReadFile(path); err == nil {
		json.Unmarshal(b, &st)
	}
	return st
}

func writeUpdateState(path string, st updateState) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	b, err := json.Marshal(st)
	if err != nil {
		return err
	}
	return os.WriteFile(path, b, 0o600)
}

func updateCheckDue(path string, interval int64) bool {
	if interval == 0 {
		interval = 86400
	}
	return updateCheckDueAt(path, time.Now().Unix(), interval)
}

func updateCheckDueAt(path string, nowUnix, interval int64) bool {
	st := readUpdateState(path)
	return nowUnix-st.CheckedAt >= interval
}

// maybeAutoUpdate is called on every CLI invocation. At most once a day (and
// only for brew-installed release builds) it spawns a detached background
// process that upgrades hcb via `brew upgrade`. Opt out with
// HCB_NO_AUTO_UPDATE=1. The foreground command is never delayed.
func maybeAutoUpdate() {
	if version == "dev" || os.Getenv("HCB_NO_AUTO_UPDATE") != "" {
		return
	}
	exe, err := os.Executable()
	if err != nil || !brewManaged(resolvePath(exe)) {
		return
	}
	path := updateStatePath()
	if path == "" {
		return
	}

	// surface the previous background update's result exactly once
	if st := readUpdateState(path); st.Notice != "" {
		fmt.Fprintln(os.Stderr, st.Notice)
		st.Notice = ""
		writeUpdateState(path, st)
	}

	if !updateCheckDue(path, 86400) {
		return
	}
	// claim the slot before spawning so concurrent invocations don't stampede
	writeUpdateState(path, updateState{CheckedAt: time.Now().Unix()})

	child := exec.Command(exe, "upgrade", "--background")
	child.Stdout, child.Stderr, child.Stdin = nil, nil, nil
	child.Start() // detached; ignore errors — next run will try again
}

// runBackgroundUpdate performs the actual check + brew upgrade. It runs in a
// detached child process so it never blocks an interactive command.
func runBackgroundUpdate() {
	path := updateStatePath()
	latest, err := fetchLatestVersion(releaseAPI)
	if err != nil || !newerVersion(version, latest) {
		return
	}
	out, err := exec.Command("brew", "upgrade", brewFormula).CombinedOutput()
	st := updateState{CheckedAt: time.Now().Unix()}
	if err != nil {
		st.Notice = fmt.Sprintf("hcb: automatic update to %s failed (run `hcb upgrade` manually): %s",
			latest, lastLine(string(out)))
	} else {
		st.Notice = fmt.Sprintf("hcb: automatically updated %s → %s via Homebrew.", version, latest)
	}
	writeUpdateState(path, st)
}

func fetchLatestVersion(apiURL string) (string, error) {
	hc := &http.Client{Timeout: 10 * time.Second}
	req, err := http.NewRequest(http.MethodGet, apiURL, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("User-Agent", "hcb-cli/"+version)
	resp, err := hc.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("release API: HTTP %d", resp.StatusCode)
	}
	var rel struct {
		TagName string `json:"tag_name"`
	}
	if err := json.Unmarshal(body, &rel); err != nil {
		return "", err
	}
	return rel.TagName, nil
}

// newerVersion reports whether latest is a strictly newer semver than current.
// Non-semver values (including "dev") never trigger an update.
func newerVersion(current, latest string) bool {
	cur, ok1 := parseSemver(current)
	lat, ok2 := parseSemver(latest)
	if !ok1 || !ok2 {
		return false
	}
	for i := 0; i < 3; i++ {
		if lat[i] != cur[i] {
			return lat[i] > cur[i]
		}
	}
	return false
}

func parseSemver(v string) ([3]int, bool) {
	v = strings.TrimPrefix(strings.TrimSpace(v), "v")
	parts := strings.SplitN(v, "-", 2)[0]
	fields := strings.Split(parts, ".")
	if len(fields) != 3 {
		return [3]int{}, false
	}
	var out [3]int
	for i, f := range fields {
		n, err := strconv.Atoi(f)
		if err != nil {
			return [3]int{}, false
		}
		out[i] = n
	}
	return out, true
}

// brewManaged reports whether the binary lives in a Homebrew prefix.
func brewManaged(exePath string) bool {
	return strings.Contains(exePath, "/Cellar/") ||
		strings.Contains(exePath, "/homebrew/") ||
		strings.Contains(exePath, "/.linuxbrew/")
}

func resolvePath(p string) string {
	if resolved, err := filepath.EvalSymlinks(p); err == nil {
		return resolved
	}
	return p
}

func lastLine(s string) string {
	lines := strings.Split(strings.TrimSpace(s), "\n")
	if len(lines) == 0 {
		return ""
	}
	return lines[len(lines)-1]
}
