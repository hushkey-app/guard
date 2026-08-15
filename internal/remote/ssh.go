// Package remote runs one command on one machine over SSH.
//
// It is the second thing guard does that reaches outwards, and much the
// sharper of the two: the prober fetches a URL, this opens a shell. So the
// package is deliberately small and has no idea what it is running — no
// allow-list of safe commands, no parsing, no shell built in. A command is a
// string somebody stored against a machine, and the only questions asked here
// are the ones a person cannot answer from the dashboard: did it connect, was
// it the same machine as last time, what came back, and did it end well.
//
// Separate from internal/telemetry for the same reason internal/cluster is:
// dialing somebody's server is not a storage package's job, and a runner that
// needed a database would be untestable against a fake server.
package remote

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"strings"
	"time"

	"golang.org/x/crypto/ssh"
)

// Login is everything needed to open one session. Fingerprint is the host key
// guard pinned the first time; empty means it has never connected.
type Login struct {
	User        string
	Address     string
	Password    string
	Fingerprint string
}

// Result is what came back. It is not an error type: a command that exited 1 is
// a result — usually the interesting one — and a caller that had to tell "the
// command failed" from "the connection failed" by reading an error string would
// get it wrong.
type Result struct {
	Output     string
	ExitCode   int
	DurationMS float64
	Truncated  bool
	// Fingerprint is the host key seen on this connection. The caller pins it
	// the first time; every later run is refused unless it matches.
	Fingerprint string
}

type Runner struct {
	// Timeout bounds the whole thing: connect, authenticate, run, read. A
	// command that hangs must not hold a request open forever, and `apt-get
	// upgrade` on a slow box is a legitimate two minutes.
	Timeout time.Duration
	// DialTimeout is separate and short. A machine that is not answering at all
	// should say so in seconds, not at the end of the command budget.
	DialTimeout time.Duration
	// MaxOutput caps what is read back. The output goes into a JSON response
	// and then into a browser; a command that prints a gigabyte should truncate
	// rather than take the process down with it.
	MaxOutput int
	Log       *slog.Logger
}

const (
	DefaultTimeout     = 2 * time.Minute
	DefaultDialTimeout = 10 * time.Second
	DefaultMaxOutput   = 256 << 10
)

// ErrHostKeyChanged is the one failure worth its own type. Everything else is
// "it did not work"; this one means the machine answering is not the machine
// that answered last time, which is either a rebuild or an interception and is
// never something to shrug at.
var ErrHostKeyChanged = errors.New("the host key changed")

func (r *Runner) prepare() {
	if r.Timeout <= 0 {
		r.Timeout = DefaultTimeout
	}
	if r.DialTimeout <= 0 {
		r.DialTimeout = DefaultDialTimeout
	}
	if r.MaxOutput <= 0 {
		r.MaxOutput = DefaultMaxOutput
	}
	if r.Log == nil {
		r.Log = slog.Default()
	}
}

// ProbeCommand is what a connection test runs: enough to prove a shell answered
// and to say which machine it was, and nothing that changes anything.
const ProbeCommand = "uname -sr; uptime"

// Probe opens a session and asks the machine to identify itself. It is the
// answer to "can guard get in", asked before anybody wires up a command that
// reboots something.
func (r *Runner) Probe(ctx context.Context, login Login) (Result, error) {
	return r.Run(ctx, login, ProbeCommand)
}

// Run executes one command and returns what it printed.
//
// Combined stdout and stderr, in the order the machine produced them: a command
// that failed explains itself on one of the two, and which one it chose is not
// a thing the reader should have to think about at 3am.
func (r *Runner) Run(ctx context.Context, login Login, command string) (Result, error) {
	r.prepare()
	if strings.TrimSpace(command) == "" {
		return Result{}, errors.New("there is no command to run")
	}
	if login.User == "" || login.Address == "" {
		return Result{}, errors.New("this machine has no ssh address")
	}
	if login.Password == "" {
		return Result{}, errors.New("this machine has no stored password")
	}

	ctx, cancel := context.WithTimeout(ctx, r.Timeout)
	defer cancel()

	started := time.Now()
	result := Result{}

	dialer := net.Dialer{Timeout: r.DialTimeout}
	conn, err := dialer.DialContext(ctx, "tcp", login.Address)
	if err != nil {
		return result, fmt.Errorf("could not reach %s: %w", login.Address, reason(err))
	}
	defer conn.Close()

	config := &ssh.ClientConfig{
		User: login.User,
		Auth: []ssh.AuthMethod{
			ssh.Password(login.Password),
			// The same password, offered the other way. Plenty of servers
			// disable PasswordAuthentication and answer with
			// keyboard-interactive instead, and to the person typing a
			// password into a form those are the same thing.
			ssh.KeyboardInteractive(func(_, _ string, questions []string, _ []bool) ([]string, error) {
				answers := make([]string, len(questions))
				for i := range answers {
					answers[i] = login.Password
				}
				return answers, nil
			}),
		},
		HostKeyCallback: func(_ string, _ net.Addr, key ssh.PublicKey) error {
			result.Fingerprint = ssh.FingerprintSHA256(key)
			if login.Fingerprint == "" {
				// Trust on first use. Guard has no way to know a host key it
				// has never seen, and refusing to connect until somebody pastes
				// one in would mean nobody ever uses this at all — the same
				// bargain ssh itself makes, minus the prompt.
				return nil
			}
			if result.Fingerprint != login.Fingerprint {
				return fmt.Errorf("%w: expected %s, got %s", ErrHostKeyChanged, login.Fingerprint, result.Fingerprint)
			}
			return nil
		},
		Timeout:       r.DialTimeout,
		ClientVersion: "SSH-2.0-guard",
	}

	// The handshake and the command both run against a raw connection, so the
	// deadline is the only thing that stops a server which accepts bytes and
	// then says nothing.
	if deadline, ok := ctx.Deadline(); ok {
		_ = conn.SetDeadline(deadline)
	}
	client, chans, reqs, err := ssh.NewClientConn(conn, login.Address, config)
	if err != nil {
		if errors.Is(err, ErrHostKeyChanged) {
			return result, err
		}
		return result, fmt.Errorf("ssh to %s@%s failed: %w", login.User, login.Address, reason(err))
	}
	session := ssh.NewClient(client, chans, reqs)
	defer session.Close()

	shell, err := session.NewSession()
	if err != nil {
		return result, fmt.Errorf("could not open a session: %w", err)
	}
	defer shell.Close()

	// Cancellation has to close the connection: an ssh session has no way to be
	// interrupted from this side, and a request that gave up should not leave a
	// goroutine reading from a dead box.
	done := make(chan struct{})
	defer close(done)
	go func() {
		select {
		case <-ctx.Done():
			conn.Close()
		case <-done:
		}
	}()

	output, runErr := shell.CombinedOutput(command)
	result.DurationMS = float64(time.Since(started)) / float64(time.Millisecond)
	if len(output) > r.MaxOutput {
		output = output[:r.MaxOutput]
		result.Truncated = true
	}
	result.Output = string(output)

	var exit *ssh.ExitError
	switch {
	case runErr == nil:
	case errors.As(runErr, &exit):
		// A non-zero exit is an answer, not a failure of this package.
		result.ExitCode = exit.ExitStatus()
	case errors.Is(ctx.Err(), context.DeadlineExceeded):
		return result, fmt.Errorf("the command did not finish within %s", r.Timeout)
	case errors.Is(runErr, io.EOF):
		return result, errors.New("the connection closed while the command was running")
	default:
		return result, runErr
	}
	return result, nil
}

// reason turns the transport's errors into something a person reading a
// dashboard can act on. "dial tcp 10.0.0.4:22: connect: connection refused"
// names the dialer, the address and the syscall, which is two facts more than
// the answer needs.
func reason(err error) error {
	text := err.Error()
	switch {
	case errors.Is(err, context.DeadlineExceeded), strings.Contains(text, "i/o timeout"):
		return errors.New("timed out")
	case strings.Contains(text, "connection refused"):
		return errors.New("connection refused — nothing is listening on that port")
	case strings.Contains(text, "no such host"):
		return errors.New("host not found")
	case strings.Contains(text, "unable to authenticate"):
		return errors.New("the password was refused")
	case strings.Contains(text, "network is unreachable"):
		return errors.New("network unreachable")
	}
	return err
}
