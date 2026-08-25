package remote

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/binary"
	"errors"
	"io"
	"net"
	"strings"
	"sync"
	"testing"

	"golang.org/x/crypto/ssh"
)

// fakeSSH is a real SSH server that runs no shell: it answers one command with
// a fixed reply and a fixed exit status.
//
// A real server rather than a mocked client, because everything worth testing
// here happens in the handshake — the host key, the password, the exit status —
// and a fake that skipped it would only be testing the fake.
type fakeSSH struct {
	address  string
	password string
	reply    string
	exit     uint32
	// commands records what was actually asked for, so a test can prove the
	// line that arrived is the line that was stored.
	commands chan string
	ptys     chan string
	signer   ssh.Signer
	listener net.Listener
	closed   sync.Once
}

func startSSH(t *testing.T, password, reply string, exit uint32) *fakeSSH {
	t.Helper()
	_, key, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	signer, err := ssh.NewSignerFromKey(key)
	if err != nil {
		t.Fatal(err)
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	server := &fakeSSH{
		address: listener.Addr().String(), password: password, reply: reply, exit: exit,
		commands: make(chan string, 4), ptys: make(chan string, 4), signer: signer, listener: listener,
	}
	t.Cleanup(server.Close)
	go server.serve()
	return server
}

func (s *fakeSSH) Close() { s.closed.Do(func() { s.listener.Close() }) }

func (s *fakeSSH) fingerprint() string { return ssh.FingerprintSHA256(s.signer.PublicKey()) }

func (s *fakeSSH) serve() {
	config := &ssh.ServerConfig{
		PasswordCallback: func(_ ssh.ConnMetadata, given []byte) (*ssh.Permissions, error) {
			if string(given) != s.password {
				return nil, errors.New("no")
			}
			return nil, nil
		},
	}
	config.AddHostKey(s.signer)
	for {
		conn, err := s.listener.Accept()
		if err != nil {
			return
		}
		go s.handle(conn, config)
	}
}

func (s *fakeSSH) handle(conn net.Conn, config *ssh.ServerConfig) {
	defer conn.Close()
	_, channels, requests, err := ssh.NewServerConn(conn, config)
	if err != nil {
		return
	}
	go ssh.DiscardRequests(requests)
	for incoming := range channels {
		if incoming.ChannelType() != "session" {
			incoming.Reject(ssh.UnknownChannelType, "no") //nolint:errcheck
			continue
		}
		channel, sessionRequests, err := incoming.Accept()
		if err != nil {
			return
		}
		for request := range sessionRequests {
			switch request.Type {
			case "pty-req":
				// RFC 4254: the terminal name is the first length-prefixed
				// string in the request payload.
				terminal := ""
				if len(request.Payload) > 4 {
					length := int(binary.BigEndian.Uint32(request.Payload[:4]))
					if len(request.Payload) >= 4+length {
						terminal = string(request.Payload[4 : 4+length])
					}
				}
				s.ptys <- terminal
				request.Reply(true, nil) //nolint:errcheck
				continue
			case "window-change":
				request.Reply(true, nil) //nolint:errcheck
				continue
			case "shell":
				request.Reply(true, nil)         //nolint:errcheck
				io.WriteString(channel, s.reply) //nolint:errcheck
				status := make([]byte, 4)
				binary.BigEndian.PutUint32(status, s.exit)
				channel.SendRequest("exit-status", false, status) //nolint:errcheck
				channel.Close()
				continue
			case "exec":
			default:
				request.Reply(false, nil) //nolint:errcheck
				continue
			}
			// The payload is a length-prefixed string, per RFC 4254.
			command := ""
			if len(request.Payload) > 4 {
				command = string(request.Payload[4:])
			}
			s.commands <- command
			request.Reply(true, nil)         //nolint:errcheck
			io.WriteString(channel, s.reply) //nolint:errcheck
			status := make([]byte, 4)
			binary.BigEndian.PutUint32(status, s.exit)
			channel.SendRequest("exit-status", false, status) //nolint:errcheck
			channel.Close()
		}
	}
}

func TestRunReturnsOutputAndPinsTheHostKey(t *testing.T) {
	server := startSSH(t, "hunter2", "Linux 6.1\n up 3 days\n", 0)
	runner := &Runner{}

	result, err := runner.Run(context.Background(), Login{
		User: "root", Address: server.address, Password: "hunter2",
	}, "uptime")
	if err != nil {
		t.Fatal(err)
	}
	if result.Output != "Linux 6.1\n up 3 days\n" || result.ExitCode != 0 {
		t.Fatalf("got %+v", result)
	}
	if got := <-server.commands; got != "uptime" {
		t.Errorf("the machine was asked for %q", got)
	}
	// Trust on first use: the caller stores this and every later connection is
	// compared against it.
	if result.Fingerprint != server.fingerprint() {
		t.Errorf("fingerprint %q, want %q", result.Fingerprint, server.fingerprint())
	}
}

func TestNonZeroExitIsAResultNotAnError(t *testing.T) {
	server := startSSH(t, "hunter2", "E: could not get lock\n", 100)
	runner := &Runner{}

	result, err := runner.Run(context.Background(), Login{
		User: "root", Address: server.address, Password: "hunter2",
	}, "apt-get upgrade -y")
	// A command that failed is the interesting case, and the output is the part
	// anybody wants — an error here would throw it away.
	if err != nil {
		t.Fatalf("a failing command errored: %v", err)
	}
	if result.ExitCode != 100 || !strings.Contains(result.Output, "could not get lock") {
		t.Fatalf("got %+v", result)
	}
}

func TestAChangedHostKeyRefusesToConnect(t *testing.T) {
	server := startSSH(t, "hunter2", "", 0)
	runner := &Runner{}

	_, err := runner.Run(context.Background(), Login{
		User: "root", Address: server.address, Password: "hunter2",
		Fingerprint: "SHA256:something-else-entirely",
	}, "uptime")
	if !errors.Is(err, ErrHostKeyChanged) {
		t.Fatalf("connected anyway, or failed for another reason: %v", err)
	}
}

func TestARefusedPasswordSaysSo(t *testing.T) {
	server := startSSH(t, "hunter2", "", 0)
	runner := &Runner{}

	_, err := runner.Run(context.Background(), Login{
		User: "root", Address: server.address, Password: "letmein",
	}, "uptime")
	if err == nil || !strings.Contains(err.Error(), "password was refused") {
		t.Fatalf("error was %v", err)
	}
}

func TestOutputIsCapped(t *testing.T) {
	server := startSSH(t, "hunter2", strings.Repeat("x", 4096), 0)
	runner := &Runner{MaxOutput: 100}

	result, err := runner.Run(context.Background(), Login{
		User: "root", Address: server.address, Password: "hunter2",
	}, "cat /dev/urandom")
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Output) != 100 || !result.Truncated {
		t.Fatalf("kept %d bytes, truncated=%v", len(result.Output), result.Truncated)
	}
}

func TestTerminalRequestsAPTY(t *testing.T) {
	server := startSSH(t, "hunter2", "\x1b[2Jeditor\r\n", 0)
	runner := &Runner{}
	var output strings.Builder

	result, err := runner.Terminal(context.Background(), Login{
		User: "root", Address: server.address, Password: "hunter2",
	}, strings.NewReader(""), &output, nil)
	if err != nil {
		t.Fatal(err)
	}
	if result.ExitCode != 0 || output.String() != "\x1b[2Jeditor\r\n" {
		t.Fatalf("got result=%+v output=%q", result, output.String())
	}
	if terminal := <-server.ptys; terminal != "xterm-256color" {
		t.Fatalf("requested terminal %q", terminal)
	}
}
