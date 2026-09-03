package server

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"mcmgr/internal/config"
)

// ringCapacity is how many console lines are kept for attach backlog.
const ringCapacity = 2000

// subBuffer is the per-subscriber queue depth. A client that stops reading has
// lines dropped rather than stalling the server's stdout pump.
const subBuffer = 512

// hub fans console lines out to attached clients and keeps a backlog.
type hub struct {
	mu     sync.Mutex
	ring   []string
	subs   map[int]chan string
	nextID int
	closed bool
}

func newHub() *hub {
	return &hub{ring: make([]string, 0, ringCapacity), subs: map[int]chan string{}}
}

// publish records a line and delivers it to every subscriber.
func (h *hub) publish(line string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.closed {
		return
	}
	if len(h.ring) == ringCapacity {
		copy(h.ring, h.ring[1:])
		h.ring[ringCapacity-1] = line
	} else {
		h.ring = append(h.ring, line)
	}
	for id, ch := range h.subs {
		select {
		case ch <- line:
		default:
			// Slow consumer: drop this line for them and keep going.
			_ = id
		}
	}
}

// subscribe returns a channel pre-loaded with the last tail lines. The channel
// is closed when the server exits.
func (h *hub) subscribe(tail int) (int, chan string) {
	h.mu.Lock()
	defer h.mu.Unlock()

	backlog := h.ring
	if tail >= 0 && tail < len(backlog) {
		backlog = backlog[len(backlog)-tail:]
	}
	ch := make(chan string, len(backlog)+subBuffer)
	for _, l := range backlog {
		ch <- l
	}
	id := h.nextID
	h.nextID++
	if h.closed {
		close(ch)
		return id, ch
	}
	h.subs[id] = ch
	return id, ch
}

func (h *hub) unsubscribe(id int) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if ch, ok := h.subs[id]; ok {
		delete(h.subs, id)
		close(ch)
	}
}

// close ends every subscription, signalling clients that the server exited.
func (h *hub) close() {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.closed {
		return
	}
	h.closed = true
	for id, ch := range h.subs {
		delete(h.subs, id)
		close(ch)
	}
}

// supervisor owns one JVM and the control listener in front of it.
type supervisor struct {
	cfg *config.Config
	st  *State
	hub *hub

	cmd     *exec.Cmd
	stdin   io.WriteCloser
	stdinMu sync.Mutex

	logFile *os.File
	logMu   sync.Mutex

	ln         net.Listener
	foreground bool

	ready    atomic.Bool
	stopping atomic.Bool
	exited   chan struct{}
	exitCode atomic.Int32
}

// Options control how a server is started.
type Options struct {
	// Foreground mirrors console output to this process's stdout and forwards
	// its stdin, for `mcmgr server run`.
	Foreground bool
	// ExtraJavaArgs are appended to the configured JVM arguments.
	ExtraJavaArgs []string
}

// Supervise runs the server to completion in the current process and returns
// the JVM's exit code. This is the body of both `server run` (foreground) and
// the detached `server _supervise` helper that `server launch` spawns.
func Supervise(cfg *config.Config, opts Options) (int, error) {
	if err := preflight(cfg); err != nil {
		return 1, err
	}

	// Refuse to start a second copy of the same server.
	if live, err := Probe(cfg.Dir()); err != nil {
		return 1, err
	} else if live != nil {
		return 1, fmt.Errorf("server %q is already running (pid %d)", cfg.Name, live.State.PID)
	}

	s := &supervisor{cfg: cfg, hub: newHub(), foreground: opts.Foreground, exited: make(chan struct{})}

	if err := os.MkdirAll(cfg.StatePath(), 0o755); err != nil {
		return 1, err
	}
	if err := s.openLog(); err != nil {
		return 1, err
	}
	defer s.logFile.Close()

	// Bind the control port before starting the JVM so a failure here does not
	// leave an unmanaged server process behind.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 1, fmt.Errorf("binding control port: %w", err)
	}
	s.ln = ln
	defer ln.Close()

	token, err := newToken()
	if err != nil {
		return 1, err
	}
	s.st = &State{
		PID:        os.Getpid(),
		Port:       ln.Addr().(*net.TCPAddr).Port,
		Token:      token,
		StartedAt:  time.Now().Unix(),
		Foreground: opts.Foreground,
		Name:       cfg.Name,
		Dir:        cfg.Dir(),
	}

	if err := s.start(opts.ExtraJavaArgs); err != nil {
		return 1, err
	}
	s.st.ChildPID = s.cmd.Process.Pid
	if err := s.st.Write(cfg.Dir()); err != nil {
		_ = s.cmd.Process.Kill()
		return 1, err
	}
	defer ClearState(cfg.Dir())

	go s.acceptLoop()
	if opts.Foreground {
		go s.mirrorToTerminal()
		go s.forwardTerminalInput()
		defer s.installSignalHandler()()
	}

	code := s.wait()
	return code, nil
}

// preflight validates that the server can actually be started, with errors
// that say what to do rather than surfacing a raw exec failure.
func preflight(cfg *config.Config) error {
	switch {
	case len(cfg.LaunchArgs) > 0:
		// Argument-file launches: verify the referenced file exists, since a
		// missing one produces an opaque JVM error.
		for _, a := range cfg.ExpandedLaunchArgs() {
			ref, ok := strings.CutPrefix(a, "@")
			if !ok {
				continue
			}
			if _, err := os.Stat(filepath.Join(cfg.Dir(), ref)); err != nil {
				return fmt.Errorf("launch argument file %s is missing; run 'mcmgr server install' to reinstall the loader", ref)
			}
		}
	case cfg.Jar == "":
		return fmt.Errorf("no server jar configured; run 'mcmgr server install' first")
	default:
		if _, err := os.Stat(filepath.Join(cfg.Dir(), cfg.Jar)); err != nil {
			return fmt.Errorf("server jar %s is missing; run 'mcmgr server install' to download it", cfg.Jar)
		}
	}
	if _, err := exec.LookPath(cfg.JavaBin()); err != nil {
		return fmt.Errorf("java not found (looked for %q): install a JDK or set java.path in %s",
			cfg.JavaBin(), config.ManifestName)
	}
	if err := checkEULA(cfg.Dir()); err != nil {
		return err
	}
	return nil
}

// checkEULA reports the missing-EULA case up front, because otherwise the
// server exits after one cryptic line.
func checkEULA(dir string) error {
	data, err := os.ReadFile(filepath.Join(dir, "eula.txt"))
	if err != nil {
		// No eula.txt yet: the server writes one on first run and exits. Let
		// it, so the user sees Mojang's own message.
		return nil
	}
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(strings.ToLower(line))
		if line == "eula=true" {
			return nil
		}
	}
	return fmt.Errorf("the Minecraft EULA has not been accepted in %s\n"+
		"       read https://aka.ms/MinecraftEULA and, if you agree, run: mcmgr server accept-eula",
		filepath.Join(dir, "eula.txt"))
}

// openLog rotates the previous console capture and opens a fresh one.
func (s *supervisor) openLog() error {
	p := ConsoleLogFor(s.cfg.Dir())
	if _, err := os.Stat(p); err == nil {
		_ = os.Remove(p + ".1")
		_ = os.Rename(p, p+".1")
	}
	f, err := os.OpenFile(p, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
	if err != nil {
		return err
	}
	s.logFile = f
	return nil
}

// Command builds the JVM invocation for a server. It is exported so
// `server status` can show exactly what would run.
func Command(cfg *config.Config, extraJava []string) *exec.Cmd {
	args := make([]string, 0, len(cfg.Java.Args)+len(extraJava)+len(cfg.ServerArgs)+2)
	args = append(args, cfg.Java.Args...)
	args = append(args, extraJava...)
	if len(cfg.LaunchArgs) > 0 {
		// NeoForge and friends boot from a JVM argument file rather than a jar.
		args = append(args, cfg.ExpandedLaunchArgs()...)
	} else {
		args = append(args, "-jar", cfg.Jar)
	}
	args = append(args, cfg.ServerArgs...)

	cmd := exec.Command(cfg.JavaBin(), args...)
	cmd.Dir = cfg.Dir()
	// The server's console is this process's pipes, never a window of its own.
	hideConsole(cmd)
	return cmd
}

// start launches the JVM and begins pumping its output into the hub.
func (s *supervisor) start(extraJava []string) error {
	s.cmd = Command(s.cfg, extraJava)

	stdin, err := s.cmd.StdinPipe()
	if err != nil {
		return err
	}
	stdout, err := s.cmd.StdoutPipe()
	if err != nil {
		return err
	}
	stderr, err := s.cmd.StderrPipe()
	if err != nil {
		return err
	}
	s.stdin = stdin

	if err := s.cmd.Start(); err != nil {
		return fmt.Errorf("starting %s: %w", s.cfg.JavaBin(), err)
	}

	s.emit(fmt.Sprintf("[mcmgr] started %s (pid %d) in %s", s.cfg.JavaBin(), s.cmd.Process.Pid, s.cfg.Dir()))

	var wg sync.WaitGroup
	wg.Add(2)
	go func() { defer wg.Done(); s.pump(stdout) }()
	go func() { defer wg.Done(); s.pump(stderr) }()

	go func() {
		// The pipes must be fully drained before Wait, which closes them.
		wg.Wait()
		err := s.cmd.Wait()
		code := 0
		if ee, ok := err.(*exec.ExitError); ok {
			code = ee.ExitCode()
		} else if err != nil {
			code = 1
		}
		s.exitCode.Store(int32(code))
		s.emit(fmt.Sprintf("[mcmgr] server exited with code %d", code))
		close(s.exited)
		s.hub.close()
	}()
	return nil
}

// readyMarker is the vanilla/Paper startup-complete line, e.g.
// `Done (12.345s)! For help, type "help"`.
const readyMarker = `)! For help, type`

// pump reads one stream line by line into the hub. It uses a Reader rather
// than a Scanner so that a mod printing a multi-megabyte line cannot kill the
// supervisor with a token-too-long error.
func (s *supervisor) pump(r io.Reader) {
	br := bufio.NewReaderSize(r, 64*1024)
	var partial strings.Builder
	for {
		chunk, isPrefix, err := br.ReadLine()
		if len(chunk) > 0 {
			partial.Write(chunk)
		}
		if isPrefix {
			continue
		}
		if partial.Len() > 0 {
			line := partial.String()
			partial.Reset()
			if !s.ready.Load() && strings.Contains(line, readyMarker) {
				s.ready.Store(true)
			}
			s.emit(line)
		}
		if err != nil {
			return
		}
	}
}

// emit writes a console line to the log file and to every attached client.
func (s *supervisor) emit(line string) {
	s.logMu.Lock()
	if s.logFile != nil {
		fmt.Fprintln(s.logFile, line)
	}
	s.logMu.Unlock()
	s.hub.publish(line)
}

// writeConsole sends a line to the server's stdin.
func (s *supervisor) writeConsole(line string) error {
	s.stdinMu.Lock()
	defer s.stdinMu.Unlock()
	if s.stdin == nil {
		return fmt.Errorf("server stdin is closed")
	}
	_, err := io.WriteString(s.stdin, line+"\n")
	return err
}

// requestStop asks the server to shut down cleanly, escalating to a kill if it
// does not exit within the grace period. It is idempotent.
func (s *supervisor) requestStop(timeoutSecs int) {
	if !s.stopping.CompareAndSwap(false, true) {
		return
	}
	if timeoutSecs <= 0 {
		timeoutSecs = s.cfg.StopTimeoutSecs()
	}
	go func() {
		s.emit("[mcmgr] stopping: saving worlds and shutting down")
		if err := s.writeConsole("stop"); err != nil {
			// If stdin is gone the console command cannot be delivered, so go
			// straight to terminating the process.
			s.emit("[mcmgr] console unavailable, terminating process")
			_ = s.cmd.Process.Kill()
			return
		}
		select {
		case <-s.exited:
		case <-time.After(time.Duration(timeoutSecs) * time.Second):
			s.emit(fmt.Sprintf("[mcmgr] did not exit within %ds, terminating", timeoutSecs))
			_ = s.cmd.Process.Kill()
		}
	}()
}

// wait blocks until the JVM exits and returns its code.
func (s *supervisor) wait() int {
	<-s.exited
	// Give attached clients a moment to receive the exit notice.
	time.Sleep(50 * time.Millisecond)
	return int(s.exitCode.Load())
}

// status builds a snapshot for the status op.
func (s *supervisor) status() *Status {
	return &Status{
		Name:      s.cfg.Name,
		Dir:       s.cfg.Dir(),
		Minecraft: s.cfg.Minecraft,
		Loader:    string(s.cfg.Loader),
		Jar:       s.cfg.Jar,
		PID:       s.st.PID,
		ChildPID:  s.st.ChildPID,
		StartedAt: s.st.StartedAt,
		Ready:     s.ready.Load(),
		Stopping:  s.stopping.Load(),
	}
}

// acceptLoop serves control connections until the listener closes.
func (s *supervisor) acceptLoop() {
	for {
		conn, err := s.ln.Accept()
		if err != nil {
			return
		}
		go s.handle(conn)
	}
}

// handle serves one control connection. A connection may issue many requests;
// an attach turns it into a streaming connection that still accepts input.
func (s *supervisor) handle(conn net.Conn) {
	defer conn.Close()

	var writeMu sync.Mutex
	send := func(r Response) error {
		data, err := json.Marshal(r)
		if err != nil {
			return err
		}
		writeMu.Lock()
		defer writeMu.Unlock()
		_, err = conn.Write(append(data, '\n'))
		return err
	}

	var (
		subID       int
		subscribed  bool
		attachDone  = make(chan struct{})
		closeAttach sync.Once
	)
	defer func() {
		if subscribed {
			s.hub.unsubscribe(subID)
		}
	}()

	br := bufio.NewReader(conn)
	for {
		raw, err := br.ReadBytes('\n')
		if err != nil {
			closeAttach.Do(func() { close(attachDone) })
			return
		}
		var req Request
		if err := json.Unmarshal(raw, &req); err != nil {
			_ = send(Response{Type: RespErr, Msg: "malformed request"})
			return
		}
		if req.Token != s.st.Token {
			_ = send(Response{Type: RespErr, Msg: "invalid control token"})
			return
		}

		switch req.Op {
		case OpPing:
			_ = send(Response{Type: RespOK})

		case OpStatus:
			_ = send(Response{Type: RespStatus, Status: s.status()})

		case OpInput:
			if err := s.writeConsole(req.Line); err != nil {
				_ = send(Response{Type: RespErr, Msg: err.Error()})
				continue
			}
			if !subscribed {
				_ = send(Response{Type: RespOK})
			}

		case OpStop:
			s.requestStop(req.Timeout)
			_ = send(Response{Type: RespOK, Msg: "stopping"})

		case OpKill:
			s.stopping.Store(true)
			s.emit("[mcmgr] killing server process")
			_ = s.cmd.Process.Kill()
			_ = send(Response{Type: RespOK, Msg: "killed"})

		case OpAttach:
			if subscribed {
				_ = send(Response{Type: RespErr, Msg: "already attached"})
				continue
			}
			// Tail: 0 means "the usual amount of context", negative means
			// "none at all", which is what a one-shot command wants so it
			// prints only the reply.
			tail := req.Tail
			switch {
			case tail < 0:
				tail = 0
			case tail == 0:
				tail = 100
			}
			var ch chan string
			subID, ch = s.hub.subscribe(tail)
			subscribed = true
			_ = send(Response{Type: RespStatus, Status: s.status()})
			go func() {
				for line := range ch {
					if err := send(Response{Type: RespLine, Line: line}); err != nil {
						return
					}
				}
				// Channel closed: either the server exited or the client
				// unsubscribed. Report the exit code either way; a detaching
				// client has already gone.
				_ = send(Response{Type: RespExit, Code: int(s.exitCode.Load())})
				conn.Close()
			}()

		default:
			_ = send(Response{Type: RespErr, Msg: "unknown op " + req.Op})
		}
	}
}

// mirrorToTerminal prints console output to stdout for foreground runs.
func (s *supervisor) mirrorToTerminal() {
	_, ch := s.hub.subscribe(0)
	out := bufio.NewWriter(os.Stdout)
	defer out.Flush()
	for line := range ch {
		fmt.Fprintln(out, line)
		out.Flush()
	}
}

// forwardTerminalInput pipes this process's stdin into the server console for
// foreground runs, so the terminal behaves like a normal server console.
func (s *supervisor) forwardTerminalInput() {
	br := bufio.NewReader(os.Stdin)
	for {
		line, err := br.ReadString('\n')
		line = strings.TrimRight(line, "\r\n")
		if line != "" {
			if werr := s.writeConsole(line); werr != nil {
				return
			}
		}
		if err != nil {
			return // stdin closed (piped input, or Ctrl-D)
		}
	}
}

// installSignalHandler makes Ctrl-C in a foreground run perform a clean
// shutdown, with a second Ctrl-C escalating to a kill. The returned function
// uninstalls the handler.
func (s *supervisor) installSignalHandler() func() {
	ch := make(chan os.Signal, 2)
	signal.Notify(ch, os.Interrupt)
	go func() {
		count := 0
		for range ch {
			count++
			if count == 1 {
				fmt.Fprintln(os.Stderr, "\n[mcmgr] interrupt: stopping server cleanly (Ctrl-C again to force)")
				s.requestStop(0)
				continue
			}
			fmt.Fprintln(os.Stderr, "[mcmgr] second interrupt: terminating")
			_ = s.cmd.Process.Kill()
			return
		}
	}()
	return func() { signal.Stop(ch); close(ch) }
}
