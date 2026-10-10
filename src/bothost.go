package main

// `lasso bot-host`: the process that owns an omp bot's stdin and stdout
// (docs/design/bots.md, "omp bots"). lasso starts it as a transient systemd
// user unit, lasso-bot-<name>, so it outlives lasso: a release restarts lasso,
// never the bot in the middle of a turn. It runs the bot's launch task
// (`mise run <task> [-- -c]`, which execs `omp --mode rpc-ui`) and serves omp's
// JSONL on a unix socket that only this user can open:
//
//   - Client lines go to omp's stdin, omp's stdout lines go to every client, so
//     a dev and a production lasso can both watch and drive the bot.
//   - On connect a client gets host_hello: the host's state, and every question
//     omp asked that nobody has answered yet (a lasso that was restarting when
//     it was asked would otherwise never see it).
//   - host_stop, host_log and host_state are the host's own; everything else is
//     omp's and passes through untouched.
//
// It relaunches omp on the same session when the bot asked to restart (the
// .lasso/restart marker, seen once the turn has ended), and otherwise exits
// with omp, leaving keep-running to lasso.

import (
	"bufio"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"net"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"
)

// botHostClientQueue bounds what a slow client may fall behind by before the
// host drops it; omp's stdout must never block on a client.
const botHostClientQueue = 4096

type botHost struct {
	name, dir, task, socket string

	mu        sync.Mutex
	clients   map[*botHostClient]bool
	stdin     io.WriteCloser
	child     *exec.Cmd
	childDone chan struct{}
	ready     bool
	streaming bool
	pending   map[string]json.RawMessage // unanswered extension_ui_request frames by id
	order     []string                   // pending ids, in the order asked
	started   time.Time
	restarts  int
	restart   bool // closing omp to relaunch it (the restart marker)
	stopping  bool
	stderr    *lineRing
	inMu      sync.Mutex // one writer on omp's stdin at a time
}

type botHostClient struct {
	conn net.Conn
	out  chan []byte
	once sync.Once
}

func (c *botHostClient) close() { c.once.Do(func() { close(c.out); c.conn.Close() }) }

// botHostFrame is the part of a frame the host reads.
type botHostFrame struct {
	Type       string `json:"type"`
	ID         string `json:"id"`
	Method     string `json:"method"`
	TargetID   string `json:"targetId"`
	IsTerminal *bool  `json:"isTerminal"`
}

func cliBotHost(args []string) {
	fs := flag.NewFlagSet("bot-host", flag.ExitOnError)
	name := fs.String("bot", "", "the bot's name")
	dir := fs.String("dir", "", "the bot's folder")
	task := fs.String("task", "bot", "the mise task that launches the bot")
	socket := fs.String("socket", "", "the unix socket to serve")
	fresh := fs.Bool("fresh", false, "start a new session instead of continuing the last one")
	_ = fs.Parse(args)
	if *name == "" || *dir == "" || *socket == "" {
		fmt.Fprintln(os.Stderr, "usage: lasso bot-host --bot <name> --dir <folder> --socket <path> [--task <task>] [--fresh]")
		os.Exit(2)
	}
	log.SetFlags(0)
	log.SetPrefix("bot-host " + *name + ": ")
	h := newBotHost(*name, *dir, *task, *socket)
	os.Exit(h.run(*fresh))
}

func newBotHost(name, dir, task, socket string) *botHost {
	return &botHost{
		name: name, dir: dir, task: task, socket: socket,
		clients: map[*botHostClient]bool{},
		pending: map[string]json.RawMessage{},
		stderr:  &lineRing{max: 200},
	}
}

// botHostCommand is the launch: the bot's task, continuing the last session
// unless fresh. A var for tests, which run a fake omp instead.
var botHostCommand = func(dir, task string, fresh bool) *exec.Cmd {
	args := []string{"run", task}
	if !fresh {
		args = append(args, "--", "-c")
	}
	cmd := exec.Command("mise", args...)
	cmd.Dir = dir
	return cmd
}

// run serves until omp exits for good or the host is stopped, and returns
// the exit code.
func (h *botHost) run(fresh bool) int {
	ln, err := h.listen()
	if err != nil {
		log.Printf("%v", err)
		return 1
	}
	defer func() {
		ln.Close()
		os.Remove(h.socket)
		h.mu.Lock()
		for c := range h.clients {
			delete(h.clients, c)
			c.close()
		}
		h.mu.Unlock()
	}()
	go h.accept(ln)

	sigs := make(chan os.Signal, 2)
	signal.Notify(sigs, syscall.SIGTERM, syscall.SIGINT, syscall.SIGHUP)
	go func() {
		<-sigs
		h.stop()
	}()
	go h.watchMarker()

	for {
		if err := h.spawn(fresh); err != nil {
			log.Printf("launch: %v", err)
			h.broadcastHost(map[string]any{"type": "host_exit", "error": err.Error()})
			return 1
		}
		fresh = false
		<-h.childDone
		code := h.child.ProcessState.ExitCode()
		h.mu.Lock()
		again := h.restart && !h.stopping
		h.restart = false
		h.ready, h.streaming = false, false
		h.pending, h.order = map[string]json.RawMessage{}, nil
		stopping := h.stopping
		h.mu.Unlock()
		if again {
			log.Printf("restarting on the same session")
			h.broadcastHost(map[string]any{"type": "host_restarting"})
			h.mu.Lock()
			h.restarts++
			h.mu.Unlock()
			continue
		}
		if stopping {
			code = 0
		}
		log.Printf("omp exited (%d)", code)
		h.broadcastHost(map[string]any{"type": "host_exit", "code": code, "stderr": h.stderr.last(20)})
		// Let the last frames reach the clients before the socket goes.
		time.Sleep(200 * time.Millisecond)
		return code
	}
}

func (h *botHost) listen() (net.Listener, error) {
	if err := os.MkdirAll(filepath.Dir(h.socket), 0o700); err != nil {
		return nil, err
	}
	_ = os.Chmod(filepath.Dir(h.socket), 0o700)
	// A socket file a dead host left behind: nobody answers it.
	if c, err := net.DialTimeout("unix", h.socket, time.Second); err == nil {
		c.Close()
		return nil, fmt.Errorf("another bot host is serving %s", h.socket)
	}
	_ = os.Remove(h.socket)
	old := syscall.Umask(0o177)
	ln, err := net.Listen("unix", h.socket)
	syscall.Umask(old)
	if err != nil {
		return nil, err
	}
	_ = os.Chmod(h.socket, 0o600)
	return ln, nil
}

func (h *botHost) spawn(fresh bool) error {
	cmd := botHostCommand(h.dir, h.task, fresh)
	if cmd.Env == nil {
		cmd.Env = botHostChildEnv(os.Environ())
	}
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Stderr = io.MultiWriter(os.Stderr, h.stderr)
	in, err := cmd.StdinPipe()
	if err != nil {
		return err
	}
	out, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	if err := cmd.Start(); err != nil {
		return err
	}
	done := make(chan struct{})
	h.mu.Lock()
	h.child, h.stdin, h.childDone, h.started = cmd, in, done, time.Now()
	h.mu.Unlock()
	go func() {
		h.pump(out)
		_ = cmd.Wait()
		close(done)
	}()
	return nil
}

// pump reads omp's stdout to EOF, tracking state and fanning each line out.
func (h *botHost) pump(out io.Reader) {
	r := bufio.NewReaderSize(out, 64*1024)
	for {
		line, err := r.ReadBytes('\n')
		if len(line) > 0 {
			h.observe(line)
			h.broadcast(line)
		}
		if err != nil {
			return
		}
	}
}

func (h *botHost) observe(line []byte) {
	var f botHostFrame
	if json.Unmarshal(line, &f) != nil {
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	switch f.Type {
	case "ready":
		h.ready = true
	case "agent_start":
		h.streaming = true
	case "agent_end":
		if f.IsTerminal == nil || *f.IsTerminal {
			h.streaming = false
		}
	case "extension_ui_request":
		switch f.Method {
		case "select", "confirm", "input", "editor":
			if f.ID != "" {
				if _, ok := h.pending[f.ID]; !ok {
					h.order = append(h.order, f.ID)
				}
				h.pending[f.ID] = append(json.RawMessage(nil), trimNL(line)...)
			}
		case "cancel":
			h.dropPending(f.TargetID)
			h.dropPending(f.ID)
		}
	}
}

func (h *botHost) dropPending(id string) {
	if id == "" {
		return
	}
	if _, ok := h.pending[id]; !ok {
		return
	}
	delete(h.pending, id)
	for i, o := range h.order {
		if o == id {
			h.order = append(h.order[:i:i], h.order[i+1:]...)
			break
		}
	}
}

func trimNL(b []byte) []byte {
	for len(b) > 0 && (b[len(b)-1] == '\n' || b[len(b)-1] == '\r') {
		b = b[:len(b)-1]
	}
	return b
}

func (h *botHost) broadcast(line []byte) {
	if len(line) == 0 || line[len(line)-1] != '\n' {
		line = append(line, '\n')
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	for c := range h.clients {
		select {
		case c.out <- line:
		default:
			// Too far behind: drop it rather than stall omp. It reconnects
			// and gets host_hello.
			delete(h.clients, c)
			c.close()
		}
	}
}

func (h *botHost) broadcastHost(v any) {
	b, _ := json.Marshal(v)
	h.broadcast(append(b, '\n'))
}

func (h *botHost) hello() []byte {
	h.mu.Lock()
	defer h.mu.Unlock()
	pending := make([]json.RawMessage, 0, len(h.order))
	for _, id := range h.order {
		pending = append(pending, h.pending[id])
	}
	childPID := 0
	if h.child != nil && h.child.Process != nil {
		childPID = h.child.Process.Pid
	}
	b, _ := json.Marshal(map[string]any{
		"type":       "host_hello",
		"bot":        h.name,
		"pid":        os.Getpid(),
		"child_pid":  childPID,
		"ready":      h.ready,
		"streaming":  h.streaming,
		"started_at": h.started.UTC().Format(time.RFC3339),
		"restarts":   h.restarts,
		"pending":    pending,
	})
	return append(b, '\n')
}

func (h *botHost) accept(ln net.Listener) {
	for {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		c := &botHostClient{conn: conn, out: make(chan []byte, botHostClientQueue)}
		c.out <- h.hello()
		h.mu.Lock()
		h.clients[c] = true
		h.mu.Unlock()
		go h.writeTo(c)
		go h.readFrom(c)
	}
}

func (h *botHost) writeTo(c *botHostClient) {
	for line := range c.out {
		_ = c.conn.SetWriteDeadline(time.Now().Add(10 * time.Second))
		if _, err := c.conn.Write(line); err != nil {
			h.mu.Lock()
			delete(h.clients, c)
			h.mu.Unlock()
			c.close()
			return
		}
	}
}

func (h *botHost) readFrom(c *botHostClient) {
	defer func() {
		h.mu.Lock()
		delete(h.clients, c)
		h.mu.Unlock()
		c.close()
	}()
	r := bufio.NewReaderSize(c.conn, 64*1024)
	for {
		line, err := r.ReadBytes('\n')
		if len(line) > 0 {
			h.handle(c, line)
		}
		if err != nil {
			return
		}
	}
}

func (h *botHost) handle(c *botHostClient, line []byte) {
	var f botHostFrame
	if json.Unmarshal(line, &f) != nil {
		return
	}
	switch f.Type {
	case "host_stop":
		go h.stop()
		return
	case "host_log":
		b, _ := json.Marshal(map[string]any{"type": "host_log", "id": f.ID, "lines": h.stderr.last(200)})
		h.sendTo(c, append(b, '\n'))
		return
	case "host_state":
		h.sendTo(c, h.hello())
		return
	case "extension_ui_response":
		h.mu.Lock()
		h.dropPending(f.ID)
		h.mu.Unlock()
		// Every client's card for this question goes away.
		h.broadcastHost(map[string]any{"type": "host_ui_answered", "id": f.ID})
	}
	if err := h.write(line); err != nil {
		b, _ := json.Marshal(map[string]any{"type": "response", "id": f.ID, "command": f.Type, "success": false, "error": "the bot is not running: " + err.Error()})
		h.sendTo(c, append(b, '\n'))
	}
}

func (h *botHost) sendTo(c *botHostClient, line []byte) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if !h.clients[c] {
		return
	}
	select {
	case c.out <- line:
	default:
	}
}

// botHostChildEnv is the host's environment without herdr's. omp is never in
// a herdr pane, and herdr's omp extension, seeing HERDR_PANE_ID, would claim
// whatever pane that names and have herdr run a second omp on this session
// when it restores that pane.
func botHostChildEnv(env []string) []string {
	out := make([]string, 0, len(env))
	for _, kv := range env {
		if strings.HasPrefix(kv, "HERDR_") {
			continue
		}
		out = append(out, kv)
	}
	return out
}

var errBotHostNoChild = errors.New("omp is not running")

func (h *botHost) write(line []byte) error {
	h.mu.Lock()
	in, ready := h.stdin, h.child != nil
	h.mu.Unlock()
	if !ready || in == nil {
		return errBotHostNoChild
	}
	if len(line) == 0 || line[len(line)-1] != '\n' {
		line = append(line, '\n')
	}
	h.inMu.Lock()
	defer h.inMu.Unlock()
	_, err := in.Write(line)
	return err
}

// watchMarker restarts omp once the bot has asked for it (mise run restart)
// and its turn is over: closing stdin is omp's own clean shutdown.
func (h *botHost) watchMarker() {
	marker := botRestartMarker(h.dir)
	t := time.NewTicker(time.Second)
	defer t.Stop()
	for range t.C {
		if _, err := os.Stat(marker); err != nil {
			continue
		}
		h.mu.Lock()
		due := h.ready && !h.streaming && !h.restart && !h.stopping
		if due {
			h.restart = true
		}
		in := h.stdin
		h.mu.Unlock()
		if !due {
			continue
		}
		_ = os.Remove(marker)
		h.broadcastHost(map[string]any{"type": "host_restarting"})
		h.inMu.Lock()
		_ = in.Close()
		h.inMu.Unlock()
		done, cmd := h.currentDone()
		go h.reap(done, cmd, 10*time.Second)
	}
}

func (h *botHost) currentDone() (chan struct{}, *exec.Cmd) {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.childDone, h.child
}

// reap kills omp's process group when it has not exited within grace of
// being asked to.
func (h *botHost) reap(done chan struct{}, cmd *exec.Cmd, grace time.Duration) {
	if cmd == nil || done == nil {
		return
	}
	select {
	case <-done:
		return
	case <-time.After(grace):
	}
	if cmd.Process != nil {
		_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGTERM)
	}
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		if cmd.Process != nil {
			_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		}
	}
}

// stop ends the bot: abort any turn, close stdin (omp exits 0 on EOF), and
// kill the group if it lingers.
func (h *botHost) stop() {
	h.mu.Lock()
	if h.stopping {
		h.mu.Unlock()
		return
	}
	h.stopping = true
	in := h.stdin
	h.mu.Unlock()
	_ = h.write([]byte(`{"type":"abort"}` + "\n"))
	if in != nil {
		h.inMu.Lock()
		_ = in.Close()
		h.inMu.Unlock()
	}
	done, cmd := h.currentDone()
	h.reap(done, cmd, 5*time.Second)
}
