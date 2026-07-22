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
)

//go:embed web
var webFS embed.FS

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
	log.Printf("terminal-cowboy listening on %s", url)
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
	HasOpEnv    bool              `json:"has_op_env"`
	Running     bool              `json:"running"`
}

// unmanagedView is a herdr session that is running (or known to herdr) but has
// no matching Terminal Cowboy project — e.g. the default session, or a
// secondary window like "barista-2" that was never saved as a project.
type unmanagedView struct {
	Name    string `json:"name"`
	Running bool   `json:"running"`
	Default bool   `json:"default"`
}

type stateResponse struct {
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
		AvailableTerminals: launcher.Detect(),
	}
	if t, err := launcher.Select(s.cfg.Global.Terminal, s.cfg.Global.WeztermBin); err != nil {
		resp.TerminalErr = err.Error()
	} else {
		resp.Terminal = t.ID
	}

	// Live herdr state; failure is non-fatal (state unknown).
	hc := &herdr.Client{Bin: s.cfg.Global.HerdrBin}
	running := map[string]bool{}
	var herdrSessions []herdr.Session
	if sessions, err := hc.List(r.Context()); err == nil {
		resp.HerdrOK = true
		herdrSessions = sessions
		running = herdr.RunningSet(sessions)
	}

	projectNames := make(map[string]bool, len(s.cfg.Sessions))
	for _, sess := range s.cfg.Sessions {
		projectNames[sess.Name] = true
	}
	// Running herdr sessions with no backing project — visible so you can see
	// and control background sessions that Terminal Cowboy didn't launch.
	for _, hs := range herdrSessions {
		if hs.Running && !projectNames[hs.Name] {
			resp.Unmanaged = append(resp.Unmanaged, unmanagedView{
				Name:    hs.Name,
				Running: hs.Running,
				Default: hs.Default,
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
		resp.Sessions = append(resp.Sessions, sessionView{
			Name:        sess.Name,
			Description: sess.Description,
			Cwd:         sess.Cwd,
			HerdrArgs:   args,
			Env:         env,
			Terminal:    sess.Terminal,
			HasOpEnv:    sess.HasOpEnv,
			Running:     running[sess.Name],
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
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "terminal": termID})
}

type attachRequest struct {
	Name string `json:"name"`
}

// handleAttach opens a window attached to an existing herdr session that has no
// backing project (an "unmanaged" session). No cwd/env/credentials are applied
// — it simply runs `herdr --session <name>`.
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
	// Bare session: just attach by name.
	termID, err := l.Launch(config.Session{Name: req.Name}, req.Name, logDir)
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
	HerdrArgs         []string          `json:"herdr_args"`
	Env               map[string]string `json:"env"`
	Terminal          string            `json:"terminal"`
	Cols              int               `json:"cols"`
	Rows              int               `json:"rows"`
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
			"herdr_args":         args,
			"env":                env,
			"terminal":           sess.Terminal,
			"cols":               sess.Cols,
			"rows":               sess.Rows,
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
			HerdrArgs:         req.HerdrArgs,
			Env:               req.Env,
			Terminal:          req.Terminal,
			Cols:              req.Cols,
			Rows:              req.Rows,
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
	Name string `json:"name"`
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
	hc := &herdr.Client{Bin: s.cfg.Global.HerdrBin}
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	if err := hc.Stop(ctx, req.Name); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

type configRequest struct {
	Addr         string `json:"addr"`
	Port         int    `json:"port"`
	Terminal     string `json:"terminal"`
	NewTab       bool   `json:"new_tab"`
	Shell        string `json:"shell"`
	NoLoginShell bool   `json:"no_login_shell"`
	Cols         int    `json:"cols"`
	Rows         int    `json:"rows"`
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
			"addr":           g.Addr,
			"port":           g.Port,
			"terminal":       g.Terminal,
			"new_tab":        g.NewTab,
			"shell":          g.Shell,
			"no_login_shell": g.NoLoginShell,
			"cols":           g.Cols,
			"rows":           g.Rows,
			"config_path":    filepath.Join(s.cfg.Root, "config.toml"),
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
