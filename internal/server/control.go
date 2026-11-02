package server

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"time"
)

// Control protocol operations.
const (
	OpPing   = "ping"   // liveness check
	OpStatus = "status" // one-shot status snapshot
	OpInput  = "input"  // write a line to the server console
	OpStop   = "stop"   // graceful shutdown, then force after the grace period
	OpKill   = "kill"   // immediate termination
	OpAttach = "attach" // stream console output until the client disconnects
	OpWake   = "wake"   // start a server idle_stop has put to sleep
)

// Response types.
const (
	RespOK     = "ok"
	RespErr    = "err"
	RespLine   = "line"
	RespStatus = "status"
	RespExit   = "exit"
)

// Request is a single client command. Every request carries the token from the
// state file; the listener binds to loopback only, and the token stops other
// local users from driving the console.
type Request struct {
	Token   string `json:"token"`
	Op      string `json:"op"`
	Line    string `json:"line,omitempty"`
	Tail    int    `json:"tail,omitempty"`
	Timeout int    `json:"timeout,omitempty"`
}

// Response is a single supervisor reply. Attach streams many of them.
type Response struct {
	Type   string  `json:"type"`
	Msg    string  `json:"msg,omitempty"`
	Line   string  `json:"line,omitempty"`
	Status *Status `json:"status,omitempty"`
	Code   int     `json:"code,omitempty"`
}

// Status is a snapshot of a running server.
type Status struct {
	Name      string `json:"name"`
	Dir       string `json:"dir"`
	Minecraft string `json:"minecraft"`
	Loader    string `json:"loader"`
	Jar       string `json:"jar"`
	PID       int    `json:"pid"`
	ChildPID  int    `json:"child_pid"`
	StartedAt int64  `json:"started_at"`
	// Ready flips once the server logs its startup-complete line.
	Ready bool `json:"ready"`
	// Stopping is true once a shutdown has been requested.
	Stopping bool `json:"stopping"`
	// Restarts counts how many times restart_on_crash has brought this
	// server back since it last ran long enough to look healthy.
	Restarts int `json:"restarts,omitempty"`
	// Asleep is true while idle_stop has the JVM stopped, waiting for a
	// player to connect.
	Asleep bool `json:"asleep,omitempty"`
	// Listen is where players connect under idle_stop, ServerPort the
	// private port the JVM is on while it runs, and Players how many are
	// connected through mcmgr.
	Listen     string `json:"listen,omitempty"`
	ServerPort int    `json:"server_port,omitempty"`
	Players    int    `json:"players,omitempty"`
}

// Conn is a client connection to a supervisor.
type Conn struct {
	c  net.Conn
	br *bufio.Reader
}

// Dial opens a control connection and authenticates implicitly by including
// the token on every request.
func Dial(st *State) (*Conn, error) {
	c, err := net.DialTimeout("tcp", st.Addr(), 3*time.Second)
	if err != nil {
		return nil, err
	}
	return &Conn{c: c, br: bufio.NewReader(c)}, nil
}

// Close releases the connection.
func (c *Conn) Close() error { return c.c.Close() }

// SetDeadline bounds subsequent reads and writes. A zero time clears it, which
// is what attach needs while it waits for console output.
func (c *Conn) SetDeadline(t time.Time) { _ = c.c.SetDeadline(t) }

// Send writes one request.
func (c *Conn) Send(r Request) error {
	data, err := json.Marshal(r)
	if err != nil {
		return err
	}
	_, err = c.c.Write(append(data, '\n'))
	return err
}

// Recv reads one response.
func (c *Conn) Recv() (*Response, error) {
	line, err := c.br.ReadBytes('\n')
	if err != nil {
		return nil, err
	}
	var resp Response
	if err := json.Unmarshal(line, &resp); err != nil {
		return nil, fmt.Errorf("malformed response from supervisor: %w", err)
	}
	if resp.Type == RespErr {
		return &resp, errors.New(resp.Msg)
	}
	return &resp, nil
}

// Do performs a single request/response round trip.
func Do(st *State, r Request) (*Response, error) {
	conn, err := Dial(st)
	if err != nil {
		return nil, err
	}
	defer conn.Close()
	conn.SetDeadline(time.Now().Add(10 * time.Second))
	r.Token = st.Token
	if err := conn.Send(r); err != nil {
		return nil, err
	}
	return conn.Recv()
}

// Live describes a server that currently has a supervisor.
type Live struct {
	State  *State
	Status *Status
	// Responsive is false when the supervisor process exists but its control
	// port is not answering, which means it is wedged rather than gone.
	Responsive bool
}

// Probe reports whether a server is running, returning (nil, nil) when it is
// not. Stale state left behind by a crash is detected and cleaned up here, so
// callers never have to reason about it.
//
// Liveness is decided in two steps: the recorded supervisor pid must still
// exist, and the control port must answer with a matching token. Requiring
// both means a recycled pid cannot be mistaken for a live server.
func Probe(dir string) (*Live, error) {
	st, err := ReadState(dir)
	if err != nil || st == nil {
		return nil, err
	}

	if !processAlive(st.PID) {
		// The supervisor is gone; the state file is leftover.
		_ = ClearState(dir)
		return nil, nil
	}

	resp, err := Do(st, Request{Op: OpStatus})
	if err != nil {
		// The pid exists but nothing is listening. It could be a recycled pid
		// (stale) or a hung supervisor. Distinguish by whether anything at all
		// accepts on the port.
		if isConnRefused(err) {
			_ = ClearState(dir)
			return nil, nil
		}
		return &Live{State: st, Responsive: false}, nil
	}
	return &Live{State: st, Status: resp.Status, Responsive: true}, nil
}

// IsRunning is the guard used before any operation that writes to the mods
// directory. It returns the live server when one exists.
func IsRunning(dir string) (bool, *Live, error) {
	live, err := Probe(dir)
	if err != nil {
		return false, nil, err
	}
	return live != nil, live, nil
}

// ErrRunning reports an operation refused because the server is up.
type ErrRunning struct {
	Name string
	Live *Live
}

func (e *ErrRunning) Error() string {
	return fmt.Sprintf("server %q is running; stop it first with 'mcmgr server stop'", e.Name)
}

// EnsureStopped returns an *ErrRunning if the server is currently up. Mod
// operations call this so a running JVM never has its jars changed underneath
// it, which is a reliable way to corrupt a world or produce a mod list that
// does not match what is actually loaded.
func EnsureStopped(dir, name string) error {
	running, live, err := IsRunning(dir)
	if err != nil {
		return err
	}
	if running {
		return &ErrRunning{Name: name, Live: live}
	}
	return nil
}

// isConnRefused reports whether a dial failed because nothing was listening,
// as opposed to timing out against a hung process.
func isConnRefused(err error) bool {
	var oe *net.OpError
	if errors.As(err, &oe) {
		var se *os.SyscallError
		if errors.As(oe.Err, &se) {
			msg := se.Err.Error()
			// "connection refused" on Unix; WSAECONNREFUSED text on Windows.
			return contains(msg, "refused") || contains(msg, "actively refused")
		}
	}
	return false
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
