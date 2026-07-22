// Terminal Cowboy — a self-contained web launcher for herdr terminal sessions.
package main

import (
	"context"
	"embed"
	"encoding/json"
	"flag"
	"fmt"
	"io/fs"
	"log"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"terminal-cowboy/internal/config"
	"terminal-cowboy/internal/herdr"
	"terminal-cowboy/internal/launcher"
	"terminal-cowboy/internal/tmux"
)

//go:embed web
var webFS embed.FS

// Version is injected at build time via -ldflags "-X main.Version=...".
// Format: YYYY-MM-DD.HHMM-<shortsha>[-dirty]. Defaults to "dev" for `go run`.
var Version = "dev"

func main() {
	var (
		addrFlag = flag.String("addr", "", "override listen address (host)")
		portFlag = flag.Int("port", 0, "override listen port")
		open     = flag.Bool("open", false, "open the UI in a browser on start")
	)
	flag.Parse()

	srv := &server{}
	if err := srv.reload(); err != nil {
		log.Fatalf("terminal-cowboy: %v", err)
	}

	addr := srv.cfg.Global.Addr
	if *addrFlag != "" {
		addr = *addrFlag
	}
	port := srv.cfg.Global.Port
	if *portFlag != 0 {
		port = *portFlag
	}
	listenAddr := net.JoinHostPort(addr, strconv.Itoa(port))

	sub, err := fs.Sub(webFS, "web")
	if err != nil {
		log.Fatal(err)
	}

	mux := http.NewServeMux()
	mux.Handle("/", http.FileServer(http.FS(sub)))
	mux.HandleFunc("/api/state", srv.handleState)
	mux.HandleFunc("/api/launch", srv.handleLaunch)
	mux.HandleFunc("/api/session", srv.handleSession)
	mux.HandleFunc("/api/stop", srv.handleStop)
	mux.HandleFunc("/api/attach", srv.handleAttach)
	mux.HandleFunc("/api/logs", srv.handleLogs)
	mux.HandleFunc("/api/config", srv.handleConfig)

	httpSrv := &http.Server{
		Addr:              listenAddr,
		Handler:           localOnly(mux),
		ReadHeaderTimeout: 5 * time.Second,
	}

	url := fmt.Sprintf("http://%s/", listenAddr)
	log.Printf("terminal-cowboy %s listening on %s", Version, url)
	log.Printf("config: %s", srv.cfg.Root)
	if *open {
		go openBrowser(url)
	}
	log.Fatal(httpSrv.ListenAndServe())
}

type server struct {
	cfg *config.Config
}

// reload re-reads config from disk so UI edits take effect without a restart.
func (s *server) reload() error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	s.cfg = cfg
	return nil
}

// localOnly rejects requests not originating from the loopback interface, since
// launching terminals is a privileged local action.
func localOnly(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		host, _, err := net.SplitHostPort(r.RemoteAddr)
		if err != nil {
			host = r.RemoteAddr
		}
		ip := net.ParseIP(host)
		if ip == nil || !ip.IsLoopback() {
			http.Error(w, "forbidden: local access only", http.StatusForbidden)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// --- API types ---

type sessionView struct {
	Name        string            `json:"name"`
	Description string            `json:"description"`
	Cwd         string            `json:"cwd"`
	HerdrArgs   []string          `json:"herdr_args"`
	Env         map[string]string `json:"env"`
	Terminal    string            `json:"terminal"`
	Runner      string            `json:"runner"`
	HasOpEnv    bool              `json:"has_op_env"`
	Running     bool              `json:"running"`
	Ephemeral   bool              `json:"ephemeral"` // no persistent session (shell) — no running dot
}

// unmanagedView is a herdr session that is running (or known to herdr) but has
// no matching Terminal Cowboy project — e.g. the default session, or a
// secondary window like "barista-2" that was never saved as a project.
type unmanagedView struct {
	Name    string `json:"name"`
	Running bool   `json:"running"`
	Default bool   `json:"default"`
	Backend string `json:"backend"` // herdr|tmux — how to attach/stop it
}

type stateResponse struct {
	Version            string          `json:"version"`
	Terminal           string          `json:"terminal"`     // selected terminal id, or "" if none
	TerminalErr        string          `json:"terminal_err"` // why selection failed, if any
	AvailableTerminals []string        `json:"available_terminals"`
	HerdrOK            bool            `json:"herdr_ok"`
	Sessions           []sessionView   `json:"sessions"`
	Unmanaged          []unmanagedView `json:"unmanaged"`
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, code int, msg string) {
	writeJSON(w, code, map[string]string{"error": msg})
}

func (s *server) handleState(w http.ResponseWriter, r *http.Request) {
	if err := s.reload(); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}

	resp := stateResponse{
		Version:            Version,
		AvailableTerminals: launcher.Detect(),
	}
	if t, err := launcher.Select(s.cfg.Global.Terminal, s.cfg.Global.WeztermBin); err != nil {
		resp.TerminalErr = err.Error()
	} else {
		resp.Terminal = t.ID
	}

	// Live state from both backends; failures are non-fatal (state unknown).
	hc := &herdr.Client{Bin: s.cfg.Global.HerdrBin}
	herdrRunning := map[string]bool{}
	var herdrSessions []herdr.Session
	if sessions, err := hc.List(r.Context()); err == nil {
		resp.HerdrOK = true
		herdrSessions = sessions
		herdrRunning = herdr.RunningSet(sessions)
	}
	tc := &tmux.Client{}
	tmuxRunning := tc.RunningSet(r.Context())

	// Track which session names each backend "owns" via a project, so unmanaged
	// listing only surfaces sessions with no backing project.
	herdrProject := map[string]bool{}
	tmuxProject := map[string]bool{}
	for _, sess := range s.cfg.Sessions {
		switch config.Backend(sess.EffectiveRunner()) {
		case "herdr":
			herdrProject[sess.SessionKey()] = true
		case "tmux":
			tmuxProject[sess.SessionKey()] = true
		}
	}

	// Unmanaged herdr sessions.
	for _, hs := range herdrSessions {
		if hs.Running && !herdrProject[hs.Name] {
			resp.Unmanaged = append(resp.Unmanaged, unmanagedView{
				Name: hs.Name, Running: true, Default: hs.Default, Backend: "herdr",
			})
		}
	}
	// Unmanaged tmux sessions (also covers sesh).
	for name := range tmuxRunning {
		if !tmuxProject[name] {
			resp.Unmanaged = append(resp.Unmanaged, unmanagedView{
				Name: name, Running: true, Backend: "tmux",
			})
		}
	}

	for _, sess := range s.cfg.Sessions {
		env := sess.Env
		if env == nil {
			env = map[string]string{}
		}
		args := sess.HerdrArgs
		if args == nil {
			args = []string{}
		}
		runner := sess.EffectiveRunner()
		backend := config.Backend(runner)
		running := false
		switch backend {
		case "herdr":
			running = herdrRunning[sess.SessionKey()]
		case "tmux":
			running = tmuxRunning[sess.SessionKey()]
		}
		resp.Sessions = append(resp.Sessions, sessionView{
			Name:        sess.Name,
			Description: sess.Description,
			Cwd:         sess.Cwd,
			HerdrArgs:   args,
			Env:         env,
			Terminal:    sess.Terminal,
			Runner:      runner,
			HasOpEnv:    sess.HasOpEnv,
			Running:     running,
			Ephemeral:   backend == "",
		})
	}
	writeJSON(w, http.StatusOK, resp)
}

type launchRequest struct {
	Name         string `json:"name"`          // project name (config identity)
	HerdrSession string `json:"herdr_session"` // optional override, e.g. "barista-2"
}

func (s *server) handleLaunch(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeErr(w, http.StatusMethodNotAllowed, "POST required")
		return
	}
	var req launchRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid JSON")
		return
	}
	if err := s.reload(); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	sess, ok := s.findSession(req.Name)
	if !ok {
		writeErr(w, http.StatusNotFound, "unknown session: "+req.Name)
		return
	}
	if req.HerdrSession != "" {
		if err := config.ValidateName(req.HerdrSession); err != nil {
			writeErr(w, http.StatusBadRequest, err.Error())
			return
		}
	}
	l, err := launcher.New(s.cfg)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	logDir, err := s.cfg.LogDir()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	termID, err := l.Launch(sess, req.HerdrSession, logDir)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}

	// Label the initial herdr workspace (herdr otherwise names it after the
	// launch directory). herdr-only; best-effort, in the background.
	if sess.EffectiveRunner() == config.RunnerHerdr && !s.cfg.Global.NoWorkspaceLabel {
		name := req.HerdrSession
		if name == "" {
			name = sess.Name
		}
		// Primary launch uses the project's custom label if set; a secondary
		// window (explicit herdr_session override) keeps the name you typed.
		label := name
		primary := req.HerdrSession == "" || req.HerdrSession == sess.Name
		if primary && sess.WorkspaceLabel != "" {
			label = sess.WorkspaceLabel
		}
		labelWorkspaceAsync(s.cfg.Global.HerdrBin, name, label, filepath.Join(logDir, name+".log"))
	}

	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "terminal": termID})
}

// labelWorkspaceAsync renames session `name`'s initial workspace to `label` in
// the background, giving herdr time to start. Failures are logged, never fatal.
func labelWorkspaceAsync(herdrBin, name, label, logPath string) {
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		hc := &herdr.Client{Bin: herdrBin}
		if err := hc.LabelInitialWorkspace(ctx, name, label); err != nil {
			if f, ferr := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600); ferr == nil {
				fmt.Fprintf(f, "workspace label skipped: %v\n", err)
				f.Close()
			}
		}
	}()
}

type attachRequest struct {
	Name    string `json:"name"`
	Backend string `json:"backend"` // herdr|tmux (default herdr)
}

// handleAttach opens a window attached to an existing session that has no
// backing project (an "unmanaged" session). No cwd/env/credentials are applied
// — it runs the backend's attach-or-create command (`herdr --session <name>`
// or `tmux new-session -A -s <name>`).
func (s *server) handleAttach(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeErr(w, http.StatusMethodNotAllowed, "POST required")
		return
	}
	var req attachRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid JSON")
		return
	}
	if err := config.ValidateName(req.Name); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	runner := config.RunnerHerdr
	if req.Backend == "tmux" {
		runner = config.RunnerTmux
	}
	l, err := launcher.New(s.cfg)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	logDir, err := s.cfg.LogDir()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	// Bare session: just attach by name via the right backend.
	termID, err := l.Launch(config.Session{Name: req.Name, Runner: runner}, req.Name, logDir)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "terminal": termID})
}

type sessionRequest struct {
	Name              string            `json:"name"`
	Description       string            `json:"description"`
	Cwd               string            `json:"cwd"`
	Runner            string            `json:"runner"`
	RunnerCmd         string            `json:"runner_cmd"`
	HerdrArgs         []string          `json:"herdr_args"`
	Env               map[string]string `json:"env"`
	Terminal          string            `json:"terminal"`
	Cols              int               `json:"cols"`
	Rows              int               `json:"rows"`
	WorkspaceLabel    string            `json:"workspace_label"`
	Remote            string            `json:"remote"`
	RemoteKeybindings string            `json:"remote_keybindings"`
	Handoff           bool              `json:"handoff"`
	OpEnv             *string           `json:"op_env"` // nil = leave unchanged; "" = remove
}

// handleSession implements GET (fetch one, incl. op_env for editing),
// POST (create/update), and DELETE.
func (s *server) handleSession(w http.ResponseWriter, r *http.Request) {
	if err := s.reload(); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	switch r.Method {
	case http.MethodGet:
		name := r.URL.Query().Get("name")
		sess, ok := s.findSession(name)
		if !ok {
			writeErr(w, http.StatusNotFound, "unknown session")
			return
		}
		opEnv, _ := s.cfg.ReadOpEnv(name)
		env := sess.Env
		if env == nil {
			env = map[string]string{}
		}
		args := sess.HerdrArgs
		if args == nil {
			args = []string{}
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"name":               sess.Name,
			"description":        sess.Description,
			"cwd":                sess.Cwd,
			"runner":             sess.EffectiveRunner(),
			"runner_cmd":         sess.RunnerCmd,
			"herdr_args":         args,
			"env":                env,
			"terminal":           sess.Terminal,
			"cols":               sess.Cols,
			"rows":               sess.Rows,
			"workspace_label":    sess.WorkspaceLabel,
			"remote":             sess.Remote,
			"remote_keybindings": sess.RemoteKeybindings,
			"handoff":            sess.Handoff,
			"op_env":             opEnv,
		})

	case http.MethodPost:
		var req sessionRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeErr(w, http.StatusBadRequest, "invalid JSON")
			return
		}
		req.Name = strings.TrimSpace(req.Name)
		if err := config.ValidateName(req.Name); err != nil {
			writeErr(w, http.StatusBadRequest, err.Error())
			return
		}
		sess := config.Session{
			Name:              req.Name,
			Description:       req.Description,
			Cwd:               req.Cwd,
			Runner:            req.Runner,
			RunnerCmd:         req.RunnerCmd,
			HerdrArgs:         req.HerdrArgs,
			Env:               req.Env,
			Terminal:          req.Terminal,
			Cols:              req.Cols,
			Rows:              req.Rows,
			WorkspaceLabel:    req.WorkspaceLabel,
			Remote:            req.Remote,
			RemoteKeybindings: req.RemoteKeybindings,
			Handoff:           req.Handoff,
		}
		if err := s.cfg.SaveSession(sess, req.OpEnv); err != nil {
			writeErr(w, http.StatusBadRequest, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true})

	case http.MethodDelete:
		name := r.URL.Query().Get("name")
		if err := s.cfg.DeleteSession(name); err != nil {
			writeErr(w, http.StatusBadRequest, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true})

	default:
		writeErr(w, http.StatusMethodNotAllowed, "unsupported method")
	}
}

type stopRequest struct {
	Name    string `json:"name"`
	Backend string `json:"backend"` // herdr|tmux; empty falls back to the project's runner
}

func (s *server) handleStop(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeErr(w, http.StatusMethodNotAllowed, "POST required")
		return
	}
	var req stopRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid JSON")
		return
	}
	if strings.TrimSpace(req.Name) == "" {
		writeErr(w, http.StatusBadRequest, "name required")
		return
	}
	// Resolve backend + actual session key. For a managed project the real
	// backend session name may differ from the project name (sesh uses the cwd
	// basename); for an unmanaged session req.Name is already the backend name.
	backend := req.Backend
	target := req.Name
	if sess, ok := s.findSession(req.Name); ok {
		if backend == "" {
			backend = config.Backend(sess.EffectiveRunner())
		}
		target = sess.SessionKey()
	} else if backend == "" {
		backend = "herdr"
	}
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()

	var err error
	switch backend {
	case "tmux":
		err = (&tmux.Client{}).Stop(ctx, target)
	default:
		err = (&herdr.Client{Bin: s.cfg.Global.HerdrBin}).Stop(ctx, target)
	}
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

type configRequest struct {
	Addr             string `json:"addr"`
	Port             int    `json:"port"`
	Terminal         string `json:"terminal"`
	NewTab           bool   `json:"new_tab"`
	Shell            string `json:"shell"`
	NoLoginShell     bool   `json:"no_login_shell"`
	Cols             int    `json:"cols"`
	Rows             int    `json:"rows"`
	NoWorkspaceLabel bool   `json:"no_workspace_label"`
}

// handleConfig gets (GET) or updates (POST) the global web-server + launch
// settings. Changing addr/port only takes effect after a restart; the response
// flags that so the UI can tell the user.
func (s *server) handleConfig(w http.ResponseWriter, r *http.Request) {
	if err := s.reload(); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	switch r.Method {
	case http.MethodGet:
		g := s.cfg.Global
		writeJSON(w, http.StatusOK, map[string]any{
			"addr":               g.Addr,
			"port":               g.Port,
			"terminal":           g.Terminal,
			"new_tab":            g.NewTab,
			"shell":              g.Shell,
			"no_login_shell":     g.NoLoginShell,
			"cols":               g.Cols,
			"rows":               g.Rows,
			"no_workspace_label": g.NoWorkspaceLabel,
			"config_path":        filepath.Join(s.cfg.Root, "config.toml"),
		})

	case http.MethodPost:
		var req configRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeErr(w, http.StatusBadRequest, "invalid JSON")
			return
		}
		if req.Port < 1 || req.Port > 65535 {
			writeErr(w, http.StatusBadRequest, "port must be between 1 and 65535")
			return
		}
		if req.Addr == "" {
			req.Addr = "127.0.0.1"
		}
		// Preserve binary-path overrides the UI doesn't manage.
		g := s.cfg.Global
		restartNeeded := g.Addr != req.Addr || g.Port != req.Port
		g.Addr = req.Addr
		g.Port = req.Port
		g.Terminal = req.Terminal
		g.NewTab = req.NewTab
		g.Shell = req.Shell
		g.NoLoginShell = req.NoLoginShell
		g.Cols = req.Cols
		g.Rows = req.Rows
		g.NoWorkspaceLabel = req.NoWorkspaceLabel
		if err := s.cfg.SaveGlobal(g); err != nil {
			writeErr(w, http.StatusInternalServerError, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"ok":             true,
			"restart_needed": restartNeeded,
		})

	default:
		writeErr(w, http.StatusMethodNotAllowed, "unsupported method")
	}
}

// handleLogs lists launch logs (no name) or returns the tail of one
// (?name=<session>&lines=N). Log names correspond to herdr session names,
// including secondary windows like "barista-2".
func (s *server) handleLogs(w http.ResponseWriter, r *http.Request) {
	logDir, err := s.cfg.LogDir()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	name := r.URL.Query().Get("name")
	if name == "" {
		entries, _ := os.ReadDir(logDir)
		var names []string
		for _, e := range entries {
			if strings.HasSuffix(e.Name(), ".log") {
				names = append(names, strings.TrimSuffix(e.Name(), ".log"))
			}
		}
		sort.Strings(names)
		writeJSON(w, http.StatusOK, map[string]any{"logs": names})
		return
	}
	if err := config.ValidateName(name); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	lines := 200
	if v := r.URL.Query().Get("lines"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 && n <= 5000 {
			lines = n
		}
	}
	data, err := os.ReadFile(filepath.Join(logDir, name+".log"))
	if err != nil {
		if os.IsNotExist(err) {
			writeJSON(w, http.StatusOK, map[string]any{"name": name, "text": "(no log yet)"})
			return
		}
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"name": name, "text": tail(string(data), lines)})
}

// tail returns the last n lines of s.
func tail(s string, n int) string {
	all := strings.Split(strings.TrimRight(s, "\n"), "\n")
	if len(all) <= n {
		return strings.Join(all, "\n")
	}
	return strings.Join(all[len(all)-n:], "\n")
}

func (s *server) findSession(name string) (config.Session, bool) {
	for _, sess := range s.cfg.Sessions {
		if sess.Name == name {
			return sess, true
		}
	}
	return config.Session{}, false
}

func openBrowser(url string) {
	time.Sleep(300 * time.Millisecond)
	candidates := [][]string{{"xdg-open", url}, {"open", url}}
	for _, c := range candidates {
		if _, err := exec.LookPath(c[0]); err == nil {
			_ = exec.Command(c[0], c[1:]...).Start()
			return
		}
	}
	_ = os.Stdout
}
