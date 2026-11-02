package server

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/Tnze/go-mc/chat"
	pk "github.com/Tnze/go-mc/net/packet"

	"mcmgr/internal/config"
	"mcmgr/internal/props"
	"mcmgr/internal/ui"
)

// Idle stop puts the supervisor in front of the server. It listens on the
// address players connect to, runs the JVM on a private port behind it, and
// passes connections through. Once nobody has been connected for the idle
// timeout the JVM is stopped through its console as usual, but the supervisor
// stays up: the next player to connect starts it again and is let in once it
// is ready. While it is asleep the server list still shows it, with a note
// saying a join will wake it.

// holdTimeout is how long a joining player is kept waiting for the server to
// come up before being told to try again. Clients give up on a silent server
// after about thirty seconds, so this stays under that.
const holdTimeout = 25 * time.Second

// handshakeTimeout bounds how long a client may take to say what it wants.
const handshakeTimeout = 10 * time.Second

// statusQueryTimeout bounds a server-list ping mcmgr sends the server itself.
const statusQueryTimeout = 3 * time.Second

// StatusCacheName keeps the server's last server-list answer, so a sleeping
// server can show its real version and icon.
const StatusCacheName = "status-cache.json"

// errShuttingDown is what a player hears when they connect during a stop.
var errShuttingDown = errors.New("the server is shutting down")

// errAsleep is returned for console input while there is no JVM to take it.
var errAsleep = errors.New("the server is asleep until a player connects; 'mcmgr server launch' wakes it")

// idleProxy is the player-facing listener of a server with idle_stop on.
type idleProxy struct {
	s       *supervisor
	ln      net.Listener
	timeout time.Duration

	// bindHost is where the JVM listens, and dialHost where the proxy
	// reaches it. hostArgs pins the JVM to loopback where the server
	// software allows it.
	bindHost string
	dialHost string
	hostArgs []string
	backend  atomic.Int32

	motd       string
	maxPlayers int

	// active counts players connected through the proxy or waiting to be,
	// and lastActive is when the server last had one, in Unix nanoseconds.
	active     atomic.Int32
	lastActive atomic.Int64

	statusMu  sync.Mutex
	status    []byte
	lastQuery time.Time
}

// newIdleProxy binds the address players connect to, which is the one
// server.properties gives the server.
func newIdleProxy(s *supervisor) (*idleProxy, error) {
	cfg := s.cfg
	p := &idleProxy{
		s:          s,
		timeout:    time.Duration(cfg.IdleTimeoutSecs()) * time.Second,
		motd:       "A Minecraft Server",
		maxPlayers: 20,
	}

	port, ip := 25565, ""
	if f, err := props.Load(cfg.Dir()); err == nil {
		if n, err := strconv.Atoi(strings.TrimSpace(f.GetOr("server-port", ""))); err == nil && n > 0 {
			port = n
		}
		ip = strings.TrimSpace(f.GetOr("server-ip", ""))
		p.motd = f.GetOr("motd", p.motd)
		if n, err := strconv.Atoi(strings.TrimSpace(f.GetOr("max-players", ""))); err == nil {
			p.maxPlayers = n
		}
		if f.Bool("prevent-proxy-connections", false) {
			s.emit("[mcmgr] warning: prevent-proxy-connections is on, but with idle_stop every player " +
				"reaches the server through mcmgr, so their address never matches; logins will fail")
		}
	}

	switch {
	case cfg.Loader == config.Paper:
		// Paper takes a bind address on the command line, so the private
		// port is not reachable from outside at all.
		p.bindHost, p.dialHost = "127.0.0.1", "127.0.0.1"
		p.hostArgs = []string{"--host", "127.0.0.1"}
	case ip == "" || ip == "0.0.0.0" || ip == "::":
		p.bindHost, p.dialHost = "", "127.0.0.1"
	default:
		p.bindHost, p.dialHost = ip, ip
	}

	addr := net.JoinHostPort(ip, strconv.Itoa(port))
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return nil, fmt.Errorf("idle_stop: listening on %s for players: %w", addr, err)
	}
	p.ln = ln
	p.status, _ = os.ReadFile(filepath.Join(cfg.StatePath(), StatusCacheName))
	p.touch()
	return p, nil
}

// addr is where players connect.
func (p *idleProxy) addr() string { return p.ln.Addr().String() }

// backendAddr is where the JVM of the current run listens.
func (p *idleProxy) backendAddr() string {
	return net.JoinHostPort(p.dialHost, strconv.Itoa(int(p.backend.Load())))
}

// serverArgs picks a free private port for the next run and returns the
// arguments that put the JVM on it. Every loader passes --port through to
// Minecraft, which takes it over server-port, so server.properties keeps
// naming the port players use.
func (p *idleProxy) serverArgs() ([]string, error) {
	ln, err := net.Listen("tcp", net.JoinHostPort(p.bindHost, "0"))
	if err != nil {
		return nil, fmt.Errorf("finding a free port for the server: %w", err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	_ = ln.Close()
	p.backend.Store(int32(port))
	return append([]string{"--port", strconv.Itoa(port)}, p.hostArgs...), nil
}

func (p *idleProxy) touch() { p.lastActive.Store(time.Now().UnixNano()) }

func (p *idleProxy) idleFor() time.Duration {
	return time.Since(time.Unix(0, p.lastActive.Load()))
}

// serve accepts players until the listener is closed.
func (p *idleProxy) serve() {
	for {
		conn, err := p.ln.Accept()
		if err != nil {
			return
		}
		go p.handle(conn)
	}
}

// handle reads what a connection wants and either answers it here or passes
// it to the server, waking the server first for a player.
func (p *idleProxy) handle(conn net.Conn) {
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(handshakeTimeout))
	br := bufio.NewReader(conn)

	first, err := br.Peek(1)
	if err != nil {
		return
	}
	if first[0] == legacyPing {
		// Only the server can answer a pre-1.7 client; asleep, it gets no
		// answer, which such a client shows as offline.
		if p.s.serving() {
			_ = p.forward(conn, br, nil)
		}
		return
	}

	c := wrapClient(conn, br)
	h, err := readHandshake(c)
	if err != nil {
		return
	}
	switch h.Intent {
	case intentStatus:
		if p.s.serving() {
			_ = p.forward(conn, br, &h.packet)
			return
		}
		_ = answerStatus(c, p.statusDoc(int32(h.Protocol)))

	case intentLogin, intentTransfer:
		p.active.Add(1)
		defer func() {
			// Touch before letting go, so the idle check never sees an empty
			// server with a stale timestamp.
			p.touch()
			p.active.Add(-1)
		}()
		if err := p.s.wake(); err != nil {
			_ = kickDuringLogin(c, fmt.Sprintf("%s could not be started: %v", p.s.cfg.Name, err))
			return
		}
		if !p.waitServing(holdTimeout) && !p.s.alive.Load() {
			_ = kickDuringLogin(c, fmt.Sprintf("%s did not start; see its console", p.s.cfg.Name))
			return
		}
		// A server not yet ready by now still gets the player: it answers
		// once it is, or the forward fails and they are told to retry.
		if err := p.forward(conn, br, &h.packet); err != nil {
			_ = kickDuringLogin(c, fmt.Sprintf("%s is still starting up; try again in a moment", p.s.cfg.Name))
		}
	}
}

// waitServing waits until the server is ready for players, and reports
// whether it got there.
func (p *idleProxy) waitServing(d time.Duration) bool {
	deadline := time.Now().Add(d)
	for !p.s.serving() {
		if time.Now().After(deadline) || p.s.stopping.Load() {
			return false
		}
		time.Sleep(200 * time.Millisecond)
	}
	return true
}

// forward connects the client to the server and copies bytes both ways until
// either side hangs up. hs is the handshake already read from the client, if
// any, which the server still needs to see. Encryption and compression are
// between the client and the server; the proxy only ever sees ciphertext.
func (p *idleProxy) forward(conn net.Conn, br *bufio.Reader, hs *pk.Packet) error {
	backend, err := net.DialTimeout("tcp", p.backendAddr(), 5*time.Second)
	if err != nil {
		return err
	}
	defer backend.Close()
	if hs != nil {
		if err := hs.Pack(backend, -1); err != nil {
			return err
		}
	}
	_ = conn.SetDeadline(time.Time{})

	done := make(chan struct{}, 2)
	go func() { _, _ = io.Copy(backend, br); done <- struct{}{} }()
	go func() { _, _ = io.Copy(conn, backend); done <- struct{}{} }()
	<-done
	// One side is gone; closing both ends the other copy too.
	conn.Close()
	backend.Close()
	<-done
	return nil
}

// remember keeps the server's own server-list answer for while it sleeps.
func (p *idleProxy) remember(doc []byte) {
	p.statusMu.Lock()
	defer p.statusMu.Unlock()
	if bytes.Equal(doc, p.status) {
		return
	}
	p.status = doc
	_ = config.WriteFileAtomic(filepath.Join(p.s.cfg.StatePath(), StatusCacheName), doc)
}

// statusDoc is the server-list answer given while the server is not up: its
// last real one where there is one, with nobody online and a line saying what
// a join will do.
func (p *idleProxy) statusDoc(clientProtocol int32) []byte {
	p.statusMu.Lock()
	cached := p.status
	p.statusMu.Unlock()

	doc := map[string]any{}
	_ = json.Unmarshal(cached, &doc)

	// A cached version from before an upgrade would show players the wrong
	// one, so it only stands while it still names the configured release.
	version, _ := doc["version"].(map[string]any)
	if name, _ := version["name"].(string); !strings.Contains(name, p.s.cfg.Minecraft) {
		doc["version"] = map[string]any{"name": p.s.cfg.Minecraft, "protocol": clientProtocol}
	}

	players, _ := doc["players"].(map[string]any)
	if players == nil {
		players = map[string]any{"max": p.maxPlayers}
	}
	players["online"] = 0
	delete(players, "sample")
	doc["players"] = players

	note := chat.Text("Asleep; join to start it").SetColor("gray")
	if p.s.alive.Load() {
		note = chat.Text("Starting up; join in a moment").SetColor("gold")
	}
	motd, _, _ := strings.Cut(p.motd, "\n")
	doc["description"] = chat.Text(motd + "\n").Append(note)

	out, _ := json.Marshal(doc)
	return out
}

// check is one tick of the idle clock: it stops the server once it has been
// empty for the timeout. Players are counted at the proxy and confirmed with
// the server itself, which also sees anyone who reached it some other way —
// a Bedrock player through Geyser, say.
func (p *idleProxy) check() {
	s := p.s
	if !s.alive.Load() || s.dozing.Load() || s.stopping.Load() {
		return
	}
	if p.active.Load() > 0 {
		p.touch()
		return
	}
	idle := p.idleFor()

	p.statusMu.Lock()
	refresh := p.status == nil && time.Since(p.lastQuery) > 30*time.Second
	if refresh || idle >= p.timeout {
		p.lastQuery = time.Now()
	}
	p.statusMu.Unlock()
	if s.ready.Load() && (refresh || idle >= p.timeout) {
		if doc, err := queryStatus(p.backendAddr(), statusQueryTimeout); err == nil {
			p.remember(doc)
			var st serverStatus
			if json.Unmarshal(doc, &st) == nil && st.Players.Online > 0 {
				p.touch()
				return
			}
		}
	}

	if idle >= p.timeout {
		s.doze()
	}
}

// idleLoop runs the idle clock until the supervisor finishes.
func (s *supervisor) idleLoop() {
	every := s.idle.timeout / 4
	if every > 5*time.Second {
		every = 5 * time.Second
	}
	if every < 250*time.Millisecond {
		every = 250 * time.Millisecond
	}
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		select {
		case <-s.exited:
			return
		case <-t.C:
			s.idle.check()
		}
	}
}

// serving reports whether players can be passed straight to the server.
func (s *supervisor) serving() bool {
	return s.alive.Load() && s.ready.Load() && !s.dozing.Load()
}

// doze stops an idle server through its console, leaving the supervisor up
// to start it again for the next player.
func (s *supervisor) doze() {
	s.runMu.Lock()
	defer s.runMu.Unlock()
	if !s.alive.Load() || s.dozing.Load() || s.stopping.Load() || s.idle.active.Load() > 0 {
		return
	}
	s.dozing.Store(true)
	s.emit(fmt.Sprintf("[mcmgr] nobody has been on for %s; stopping the server until a player connects",
		ui.Duration(s.idle.timeout)))

	done := s.currentRun()
	if err := s.writeConsole("stop"); err != nil {
		s.kill()
		return
	}
	timeout := s.cfg.StopTimeoutSecs()
	go func() {
		select {
		case <-done:
		case <-time.After(time.Duration(timeout) * time.Second):
			s.emit(fmt.Sprintf("[mcmgr] still running %ds after 'stop', terminating (raise stop_timeout_secs if saves take longer)", timeout))
			s.kill()
		}
	}()
}

// sleepAfterExit takes a server with idle_stop to sleep when its JVM exits
// for any reason but a stop mcmgr was asked for, and reports whether it did.
// Whether the exit was an idle stop, a crash past its restarts or someone
// typing 'stop' in the console, the next player starts it again.
func (s *supervisor) sleepAfterExit(code int) bool {
	if s.idle == nil {
		return false
	}
	s.runMu.Lock()
	defer s.runMu.Unlock()
	if s.stopping.Load() {
		return false
	}
	wasDozing := s.dozing.Swap(false)
	s.asleep.Store(true)
	s.ready.Store(false)
	s.stdinMu.Lock()
	s.stdin = nil
	s.stdinMu.Unlock()

	s.stMu.Lock()
	s.st.ChildPID = 0
	_ = s.st.Write(s.cfg.Dir())
	s.stMu.Unlock()

	why := "server stopped"
	if !wasDozing {
		why = fmt.Sprintf("server exited with code %d", code)
	}
	s.emit(fmt.Sprintf("[mcmgr] %s; listening on %s and starting it again when a player connects", why, s.idle.addr()))
	return true
}

// wake starts a sleeping server. It returns once the JVM is launched, not
// once it is ready; a server that is already up is left alone.
func (s *supervisor) wake() error {
	for {
		s.runMu.Lock()
		switch {
		case s.stopping.Load():
			s.runMu.Unlock()
			return errShuttingDown
		case s.dozing.Load():
			// It is on its way down; let it finish, then start it fresh.
			done := s.currentRun()
			s.runMu.Unlock()
			<-done
			continue
		case !s.asleep.Load():
			s.runMu.Unlock()
			return nil
		}

		s.emit("[mcmgr] a player is connecting; starting the server")
		err := s.start(s.opts.ExtraJavaArgs)
		if err != nil {
			s.emit(fmt.Sprintf("[mcmgr] could not start the server: %v", err))
		} else {
			s.asleep.Store(false)
			s.stMu.Lock()
			s.st.ChildPID = s.process().Pid
			_ = s.st.Write(s.cfg.Dir())
			s.stMu.Unlock()
		}
		s.runMu.Unlock()
		return err
	}
}

// finishIfAsleep ends a sleeping supervisor outright, since there is no JVM
// to stop, and reports whether it did.
func (s *supervisor) finishIfAsleep() bool {
	s.runMu.Lock()
	defer s.runMu.Unlock()
	if !s.asleep.Load() {
		return false
	}
	s.emit("[mcmgr] stopped while asleep")
	s.finish()
	return true
}
