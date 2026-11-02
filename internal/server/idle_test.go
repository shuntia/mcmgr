package server

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	mcnet "github.com/Tnze/go-mc/net"
	pk "github.com/Tnze/go-mc/net/packet"

	"mcmgr/internal/config"
)

// fakeJVMEnv turns the test binary into a stand-in for a Minecraft server, so
// the supervisor can be driven end to end without Java.
const fakeJVMEnv = "MCMGR_TEST_FAKE_JVM"

func TestMain(m *testing.M) {
	if os.Getenv(fakeJVMEnv) == "1" {
		fakeJVM(os.Args[1:])
		return
	}
	os.Exit(m.Run())
}

// fakeJVM answers `java -version`, or behaves like a server: it listens on
// --port, prints the startup-complete line, answers server-list pings, echoes
// whatever a player sends after the handshake, and exits on "stop".
func fakeJVM(args []string) {
	port := ""
	for i, a := range args {
		switch a {
		case "-version":
			fmt.Fprintln(os.Stderr, `openjdk version "21.0.1" 2023-10-17`)
			os.Exit(0)
		case "--port":
			port = args[i+1]
		}
	}
	ln, err := net.Listen("tcp", net.JoinHostPort("127.0.0.1", port))
	if err != nil {
		fmt.Println("FAILED TO BIND TO PORT", err)
		os.Exit(1)
	}
	fmt.Println(`[Server thread/INFO]: Done (0.1s)! For help, type "help"`)
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go func() {
				defer conn.Close()
				br := bufio.NewReader(conn)
				c := wrapClient(conn, br)
				h, err := readHandshake(c)
				if err != nil {
					return
				}
				if h.Intent == intentStatus {
					_ = answerStatus(c, []byte(`{"version":{"name":"1.21.4","protocol":769},`+
						`"players":{"max":5,"online":0},"description":"the real server"}`))
					return
				}
				_, _ = io.Copy(conn, br)
			}()
		}
	}()
	sc := bufio.NewScanner(os.Stdin)
	for sc.Scan() {
		if strings.TrimSpace(sc.Text()) == "stop" {
			os.Exit(0)
		}
	}
}

func freePort(t *testing.T) int {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	return ln.Addr().(*net.TCPAddr).Port
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

func liveStatus(dir string) *Status {
	live, _ := Probe(dir)
	if live == nil {
		return nil
	}
	return live.Status
}

func description(t *testing.T, addr string) string {
	t.Helper()
	doc, err := queryStatus(addr, 3*time.Second)
	if err != nil {
		return ""
	}
	var d struct {
		Description json.RawMessage `json:"description"`
	}
	_ = json.Unmarshal(doc, &d)
	return string(d.Description)
}

// TestIdleStopSleepsAndWakes drives the whole cycle: the server stops once
// nobody is on, the server list still answers while it sleeps, a joining
// player wakes it and is passed through, and it does not stop under them.
func TestIdleStopSleepsAndWakes(t *testing.T) {
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv(fakeJVMEnv, "1")

	dir := t.TempDir()
	port := freePort(t)
	addr := net.JoinHostPort("127.0.0.1", strconv.Itoa(port))
	props := fmt.Sprintf("server-port=%d\nserver-ip=127.0.0.1\nmotd=Test World\n", port)
	if err := os.WriteFile(filepath.Join(dir, "server.properties"), []byte(props), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "server.jar"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	cfg := &config.Config{
		Name: "idle", Minecraft: "1.21.4", Loader: config.Vanilla, Jar: "server.jar",
		Java: config.Java{Path: exe}, IdleStop: true, IdleTimeout: 1, StopTimeout: 5,
	}
	cfg.SetDir(dir)

	type result struct {
		code int
		err  error
	}
	done := make(chan result, 1)
	go func() {
		code, err := Supervise(cfg, Options{})
		done <- result{code, err}
	}()

	// Up first, answering for itself.
	waitFor(t, "the server to answer through the proxy", func() bool {
		return strings.Contains(description(t, addr), "the real server")
	})

	// Nobody joins, so it goes to sleep, and the list still shows it.
	waitFor(t, "the server to fall asleep", func() bool {
		st := liveStatus(dir)
		return st != nil && st.Asleep
	})
	if d := description(t, addr); !strings.Contains(d, "Asleep") || !strings.Contains(d, "Test World") {
		t.Errorf("asleep server-list answer = %s", d)
	}

	// A player joins: the server wakes and the connection is passed through.
	conn, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatal(err)
	}
	c := mcnet.WrapConn(conn)
	if err := c.WritePacket(pk.Marshal(0x00,
		pk.VarInt(769), pk.String("127.0.0.1"), pk.UnsignedShort(port), pk.VarInt(intentLogin))); err != nil {
		t.Fatal(err)
	}
	if _, err := conn.Write([]byte("hello\n")); err != nil {
		t.Fatal(err)
	}
	_ = conn.SetReadDeadline(time.Now().Add(15 * time.Second))
	line, err := bufio.NewReader(conn).ReadString('\n')
	if err != nil || line != "hello\n" {
		t.Fatalf("player traffic was not passed through: %q, %v", line, err)
	}
	if st := liveStatus(dir); st == nil || st.Asleep || st.Players != 1 {
		t.Fatalf("with a player on, status = %+v", st)
	}

	// Well past the idle timeout, the server stays up while they are on.
	time.Sleep(2500 * time.Millisecond)
	if st := liveStatus(dir); st == nil || st.Asleep {
		t.Fatal("the server was stopped with a player still connected")
	}

	// They leave, and it sleeps again.
	conn.Close()
	waitFor(t, "the server to fall asleep after the player left", func() bool {
		st := liveStatus(dir)
		return st != nil && st.Asleep
	})

	// A console command has nowhere to go, and says so.
	live, _ := Probe(dir)
	if err := SendCommand(live, "list"); err == nil || !strings.Contains(err.Error(), "asleep") {
		t.Errorf("a command to a sleeping server: %v", err)
	}

	// A stop while asleep ends the supervisor without waiting on a JVM.
	if _, err := Do(live.State, Request{Op: OpStop}); err != nil {
		t.Fatal(err)
	}
	select {
	case r := <-done:
		if r.err != nil {
			t.Fatal(r.err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the supervisor did not finish after a stop while asleep")
	}
	if _, err := net.Dial("tcp", addr); err == nil {
		t.Error("the player port is still open after the supervisor finished")
	}
}

// TestIdleStopDoesNotRestartAnIdleStop keeps restart_on_crash from undoing an
// idle stop that had to be escalated to a kill.
func TestIdleStopDoesNotRestartAnIdleStop(t *testing.T) {
	cfg := &config.Config{Name: "test", RestartOnCrash: true, IdleStop: true}
	cfg.SetDir(t.TempDir())
	s := newRestartSupervisor(cfg)
	s.dozing.Store(true)
	if _, ok := s.shouldRestart(137, time.Minute); ok {
		t.Error("a killed idle stop must not be treated as a crash")
	}
}
