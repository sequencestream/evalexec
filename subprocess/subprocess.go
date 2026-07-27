// Package subprocess runs a line-oriented external process: one JSON request
// in, one JSON response out.
//
// It is shared by the stdio-jsonl Grader and the stdio-jsonl Judge, which speak
// different payloads over an identical transport.
//
// Three details here are the difference between working and hanging:
//
//   - stderr is drained continuously by its own goroutine. A process that fills
//     the stderr pipe buffer blocks on the write while we wait on stdout, and
//     nothing ever moves again.
//   - cancellation kills the process group, not the process. A script that
//     forked — a Python wrapper, a shell pipeline — would otherwise leave
//     orphans behind holding the pipe open.
//   - the response line limit matches the dataset's, because an Evaluation
//     carrying evidence is not small and the default scanner limit of 64 KB is
//     easy to exceed.
//
// # Stability
//
// L3 component. Changeable during v0; from v1.0 it follows the Go
// compatibility promise.
package subprocess

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"sync"
	"syscall"
)

// maxLineBytes caps one response line at 32 MB, matching the dataset reader.
const maxLineBytes = 32 << 20

// maxStderrBytes is how much of a process's stderr is retained for diagnosis.
// The tail is what matters: a crash reports itself at the end.
const maxStderrBytes = 64 << 10

// ErrClosed reports use of a process that has been shut down.
var ErrClosed = errors.New("subprocess: the process is closed")

// ErrNoResponse reports a process that produced no line before exiting.
var ErrNoResponse = errors.New("subprocess: the process produced no response")

// Process is one running external program, exchanging one line per call.
//
// It is safe for concurrent use only in the sense that calls are serialized:
// the protocol is one question at a time, so a Pool gives each worker its own
// process rather than sharing one.
type Process struct {
	command string
	args    []string

	mu     sync.Mutex
	cmd    *exec.Cmd
	stdin  io.WriteCloser
	stdout *bufio.Reader
	stderr *tailBuffer
	closed bool
}

// New prepares a process description. Nothing is started until the first call.
func New(command string, args ...string) *Process {
	return &Process{command: command, args: args}
}

// Call sends one request line and reads one response line.
func (p *Process) Call(ctx context.Context, request []byte) ([]byte, error) {
	p.mu.Lock()
	defer p.mu.Unlock()

	if p.closed {
		return nil, ErrClosed
	}

	if err := p.ensureStarted(); err != nil {
		return nil, err
	}

	// A cancelled call kills the process: there is no way to abandon a
	// half-finished exchange on a pipe and reuse it, because the response to
	// the abandoned request would be read as the answer to the next one.
	done := make(chan struct{})
	defer close(done)

	go func() {
		select {
		case <-ctx.Done():
			_ = p.killLocked()
		case <-done:
		}
	}()

	line, err := p.exchange(request)
	if err != nil {
		// A cancellation is reported as such, not as whatever I/O error the
		// kill produced, so the caller can tell an abandoned sample from a
		// broken Grader.
		if ctxErr := ctx.Err(); ctxErr != nil {
			return nil, ctxErr
		}

		return nil, err
	}

	return line, nil
}

// exchange writes the request and reads the reply.
func (p *Process) exchange(request []byte) ([]byte, error) {
	if _, err := p.stdin.Write(append(bytes.TrimRight(request, "\n"), '\n')); err != nil {
		return nil, fmt.Errorf("subprocess: write to %s: %w (stderr: %s)", p.command, err, p.stderr.String())
	}

	line, err := p.readLine()
	if err != nil {
		return nil, err
	}

	return line, nil
}

// readLine reads one line, growing past the scanner default so a large
// Evaluation is not silently truncated.
func (p *Process) readLine() ([]byte, error) {
	var out []byte

	for {
		chunk, err := p.stdout.ReadSlice('\n')
		out = append(out, chunk...)

		switch {
		case err == nil:
			return bytes.TrimRight(out, "\r\n"), nil
		case errors.Is(err, bufio.ErrBufferFull):
			if len(out) > maxLineBytes {
				return nil, fmt.Errorf("subprocess: %s wrote a line over the %d byte limit", p.command, maxLineBytes)
			}

			continue
		case errors.Is(err, io.EOF):
			if len(out) > 0 {
				// A final line with no newline is still an answer.
				return bytes.TrimRight(out, "\r\n"), nil
			}

			return nil, fmt.Errorf("%w: %s exited (stderr: %s)", ErrNoResponse, p.command, p.stderr.String())
		default:
			return nil, fmt.Errorf("subprocess: read from %s: %w (stderr: %s)", p.command, err, p.stderr.String())
		}
	}
}

// ensureStarted launches the process if it is not already running.
func (p *Process) ensureStarted() error {
	if p.cmd != nil {
		return nil
	}

	// exec.Command rather than CommandContext on purpose: the process is
	// pooled and outlives any single call, so binding its lifetime to one
	// sample's context would kill it after the first sample. Cancellation is
	// handled per call in Call, which kills the process group.
	//
	//nolint:gosec,noctx // the command is the run's own configuration; lifetime is managed in Call
	cmd := exec.Command(p.command, p.args...)
	// Its own process group, so cancellation can take the whole tree.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}

	stdin, err := cmd.StdinPipe()
	if err != nil {
		return fmt.Errorf("subprocess: stdin for %s: %w", p.command, err)
	}

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return fmt.Errorf("subprocess: stdout for %s: %w", p.command, err)
	}

	stderrPipe, err := cmd.StderrPipe()
	if err != nil {
		return fmt.Errorf("subprocess: stderr for %s: %w", p.command, err)
	}

	if err := cmd.Start(); err != nil {
		return fmt.Errorf("subprocess: cannot start %s: %w", p.command, err)
	}

	p.cmd = cmd
	p.stdin = stdin
	p.stdout = bufio.NewReaderSize(stdout, 64*1024)
	p.stderr = newTailBuffer(maxStderrBytes)

	// Drain stderr continuously. Without this a chatty process fills the pipe
	// buffer, blocks on write, and never answers the request we are waiting for.
	go func() {
		_, _ = io.Copy(p.stderr, stderrPipe)
	}()

	return nil
}

// Stderr returns the retained tail of the process's diagnostic output.
func (p *Process) Stderr() string {
	p.mu.Lock()
	defer p.mu.Unlock()

	if p.stderr == nil {
		return ""
	}

	return p.stderr.String()
}

// Close shuts the process down.
func (p *Process) Close() error {
	p.mu.Lock()
	defer p.mu.Unlock()

	p.closed = true

	return p.killLocked()
}

// killLocked ends the process group. The caller holds the lock, except on the
// cancellation path where the process is being torn down regardless.
func (p *Process) killLocked() error {
	if p.cmd == nil || p.cmd.Process == nil {
		return nil
	}

	// Closing stdin first gives a well-behaved process the chance to exit on
	// its own, which is tidier than killing one that was about to finish.
	if p.stdin != nil {
		_ = p.stdin.Close()
	}

	pid := p.cmd.Process.Pid

	// Negative pid means the process group: a script that forked leaves
	// orphans otherwise, and an orphan holding the pipe open is indistinguishable
	// from a process that has not answered yet.
	if err := syscall.Kill(-pid, syscall.SIGKILL); err != nil && !errors.Is(err, syscall.ESRCH) {
		// Fall back to the process itself; the group may already be gone.
		_ = p.cmd.Process.Kill()
	}

	_ = p.cmd.Wait()
	p.cmd = nil

	return nil
}

// Pool hands each worker its own process, because the protocol is one exchange
// at a time and sharing would interleave conversations.
//
// The process count therefore equals the run's concurrency. That is documented
// rather than hidden: it is the caller's machine that has to accommodate it.
type Pool struct {
	command string
	args    []string

	mu     sync.Mutex
	idle   []*Process
	all    []*Process
	closed bool
}

// NewPool prepares a pool for a command.
func NewPool(command string, args ...string) *Pool {
	return &Pool{command: command, args: args}
}

// Acquire takes a process from the pool, starting a new one if none is idle.
func (p *Pool) Acquire() (*Process, error) {
	p.mu.Lock()
	defer p.mu.Unlock()

	if p.closed {
		return nil, ErrClosed
	}

	if n := len(p.idle); n > 0 {
		proc := p.idle[n-1]
		p.idle = p.idle[:n-1]

		return proc, nil
	}

	proc := New(p.command, p.args...)
	p.all = append(p.all, proc)

	return proc, nil
}

// Release returns a process to the pool.
//
// A process that failed is not reused: after a killed or crashed exchange there
// is no way to know whether the pipe still holds an unread answer, and reusing
// it would attribute one sample's verdict to another.
func (p *Pool) Release(proc *Process, failed bool) {
	p.mu.Lock()
	defer p.mu.Unlock()

	if failed {
		_ = proc.Close()

		return
	}

	if p.closed {
		_ = proc.Close()

		return
	}

	p.idle = append(p.idle, proc)
}

// Close shuts every process down.
func (p *Pool) Close() error {
	p.mu.Lock()
	defer p.mu.Unlock()

	p.closed = true

	for _, proc := range p.all {
		_ = proc.Close()
	}

	p.all, p.idle = nil, nil

	return nil
}

// Size reports how many processes the pool has started, for tests that assert
// the count matches the concurrency.
func (p *Pool) Size() int {
	p.mu.Lock()
	defer p.mu.Unlock()

	return len(p.all)
}

// tailBuffer keeps the last n bytes written to it.
type tailBuffer struct {
	mu    sync.Mutex
	limit int
	buf   []byte
}

func newTailBuffer(limit int) *tailBuffer {
	return &tailBuffer{limit: limit}
}

func (b *tailBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()

	b.buf = append(b.buf, p...)
	if len(b.buf) > b.limit {
		b.buf = b.buf[len(b.buf)-b.limit:]
	}

	return len(p), nil
}

func (b *tailBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()

	return string(b.buf)
}

// Executable reports whether a path names a file this process can run, so a
// misconfigured Grader fails during the pre-check rather than on the first
// sample.
func Executable(path string) error {
	info, err := os.Stat(path)
	if err != nil {
		return fmt.Errorf("subprocess: cannot use %s: %w", path, err)
	}

	if info.IsDir() {
		return fmt.Errorf("subprocess: %s is a directory, not an executable", path)
	}

	if info.Mode().Perm()&0o111 == 0 {
		return fmt.Errorf("subprocess: %s is not executable", path)
	}

	return nil
}
