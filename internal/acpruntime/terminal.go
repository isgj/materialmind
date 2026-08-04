package acpruntime

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	acp "github.com/coder/acp-go-sdk"
	"github.com/google/uuid"
)

const (
	defaultTerminalOutputBytes = 1024 * 1024
	maxTerminalOutputBytes     = 16 * 1024 * 1024
	maxLiveTerminalOutputBytes = 512 * 1024
)

type clientTerminal struct {
	id        string
	sessionID acp.SessionId
	command   *exec.Cmd
	client    *client
	limit     int
	done      chan struct{}

	mu         sync.Mutex
	output     []byte
	truncated  bool
	liveBytes  int
	exitStatus *acp.TerminalExitStatus
}

type terminalWriter struct {
	terminal *clientTerminal
	stream   string
}

func (writer terminalWriter) Write(data []byte) (int, error) {
	return writer.terminal.write(writer.stream, data)
}

func (c *client) createTerminal(
	_ context.Context,
	request acp.CreateTerminalRequest,
) (acp.CreateTerminalResponse, error) {
	session, ok := c.session(request.SessionId)
	if !ok || session.handler == nil {
		return acp.CreateTerminalResponse{}, fmt.Errorf(
			"create ACP terminal: session %q has no active prompt",
			request.SessionId,
		)
	}
	if err := session.ctx.Err(); err != nil {
		return acp.CreateTerminalResponse{}, err
	}
	environment, err := terminalEnvironment(request.Env)
	if err != nil {
		return acp.CreateTerminalResponse{}, err
	}
	workingDirectory := session.workingDirectory
	if request.Cwd != nil {
		workingDirectory = *request.Cwd
	}
	outputLimit, err := terminalOutputLimit(request.OutputByteLimit)
	if err != nil {
		return acp.CreateTerminalResponse{}, err
	}

	command := exec.CommandContext(session.ctx, request.Command, request.Args...)
	command.Dir = workingDirectory
	command.Env = environment
	command.WaitDelay = time.Second
	configureProcess(command)

	terminal := &clientTerminal{
		id:        uuid.NewString(),
		sessionID: request.SessionId,
		command:   command,
		client:    c,
		limit:     outputLimit,
		done:      make(chan struct{}),
	}
	command.Stdout = terminalWriter{terminal: terminal, stream: "stdout"}
	command.Stderr = terminalWriter{terminal: terminal, stream: "stderr"}
	if err := command.Start(); err != nil {
		return acp.CreateTerminalResponse{}, fmt.Errorf(
			"start ACP terminal command %q: %w",
			request.Command,
			err,
		)
	}

	c.terminalMu.Lock()
	c.terminals[terminal.id] = terminal
	c.terminalMu.Unlock()
	go terminal.wait()
	return acp.CreateTerminalResponse{TerminalId: terminal.id}, nil
}

func (c *client) killTerminal(
	_ context.Context,
	request acp.KillTerminalRequest,
) (acp.KillTerminalResponse, error) {
	terminal, err := c.getTerminal(request.SessionId, request.TerminalId)
	if err != nil {
		return acp.KillTerminalResponse{}, err
	}
	terminal.kill()
	return acp.KillTerminalResponse{}, nil
}

func (c *client) terminalOutput(
	_ context.Context,
	request acp.TerminalOutputRequest,
) (acp.TerminalOutputResponse, error) {
	terminal, err := c.getTerminal(request.SessionId, request.TerminalId)
	if err != nil {
		return acp.TerminalOutputResponse{}, err
	}
	return terminal.snapshot(), nil
}

func (c *client) releaseTerminal(
	_ context.Context,
	request acp.ReleaseTerminalRequest,
) (acp.ReleaseTerminalResponse, error) {
	c.terminalMu.Lock()
	terminal := c.terminals[request.TerminalId]
	if terminal == nil || terminal.sessionID != request.SessionId {
		c.terminalMu.Unlock()
		return acp.ReleaseTerminalResponse{}, terminalNotFound(request.TerminalId)
	}
	delete(c.terminals, request.TerminalId)
	c.terminalMu.Unlock()
	terminal.kill()
	return acp.ReleaseTerminalResponse{}, nil
}

func (c *client) waitForTerminalExit(
	ctx context.Context,
	request acp.WaitForTerminalExitRequest,
) (acp.WaitForTerminalExitResponse, error) {
	terminal, err := c.getTerminal(request.SessionId, request.TerminalId)
	if err != nil {
		return acp.WaitForTerminalExitResponse{}, err
	}
	select {
	case <-terminal.done:
	case <-ctx.Done():
		return acp.WaitForTerminalExitResponse{}, ctx.Err()
	}
	status := terminal.status()
	return acp.WaitForTerminalExitResponse{
		ExitCode: status.ExitCode,
		Signal:   status.Signal,
	}, nil
}

func (c *client) getTerminal(
	sessionID acp.SessionId,
	terminalID string,
) (*clientTerminal, error) {
	c.terminalMu.RLock()
	defer c.terminalMu.RUnlock()
	terminal := c.terminals[terminalID]
	if terminal == nil || terminal.sessionID != sessionID {
		return nil, terminalNotFound(terminalID)
	}
	return terminal, nil
}

func (c *client) closeTerminals() {
	c.terminalMu.Lock()
	terminals := make([]*clientTerminal, 0, len(c.terminals))
	for _, terminal := range c.terminals {
		terminals = append(terminals, terminal)
	}
	clear(c.terminals)
	c.terminalMu.Unlock()
	for _, terminal := range terminals {
		terminal.kill()
	}
}

func (c *client) closeSessionTerminals(sessionID acp.SessionId) {
	c.terminalMu.Lock()
	terminals := make([]*clientTerminal, 0)
	for terminalID, terminal := range c.terminals {
		if terminal.sessionID != sessionID {
			continue
		}
		terminals = append(terminals, terminal)
		delete(c.terminals, terminalID)
	}
	c.terminalMu.Unlock()
	for _, terminal := range terminals {
		terminal.kill()
	}
}

func (c *client) emitTerminalOutput(event TerminalOutputEvent, sessionID acp.SessionId) {
	handler, ok := c.handler(sessionID).(TerminalOutputHandler)
	if ok {
		handler.TerminalOutput(event)
	}
}

func (terminal *clientTerminal) write(stream string, data []byte) (int, error) {
	terminal.mu.Lock()
	written := len(data)
	terminal.output = append(terminal.output, data...)
	if len(terminal.output) > terminal.limit {
		terminal.truncated = true
		terminal.output = trimTerminalOutput(terminal.output, terminal.limit)
	}
	live := data
	if terminal.liveBytes >= maxLiveTerminalOutputBytes {
		live = nil
	} else if remaining := maxLiveTerminalOutputBytes - terminal.liveBytes; len(live) > remaining {
		live = live[:remaining]
	}
	terminal.liveBytes += len(live)
	terminal.mu.Unlock()

	if len(live) > 0 {
		terminal.client.emitTerminalOutput(TerminalOutputEvent{
			TerminalID: terminal.id,
			Stream:     stream,
			Text:       cleanTerminalOutput(live),
		}, terminal.sessionID)
	}
	return written, nil
}

func (terminal *clientTerminal) wait() {
	err := terminal.command.Wait()
	stopProcess(terminal.command)
	status := terminalProcessExitStatus(terminal.command.ProcessState, err)
	terminal.mu.Lock()
	terminal.exitStatus = &status
	close(terminal.done)
	terminal.mu.Unlock()
}

func (terminal *clientTerminal) kill() {
	select {
	case <-terminal.done:
		return
	default:
		stopProcess(terminal.command)
	}
}

func (terminal *clientTerminal) snapshot() acp.TerminalOutputResponse {
	terminal.mu.Lock()
	defer terminal.mu.Unlock()
	return acp.TerminalOutputResponse{
		Output:     cleanTerminalOutput(terminal.output),
		Truncated:  terminal.truncated,
		ExitStatus: cloneTerminalExitStatus(terminal.exitStatus),
	}
}

func (terminal *clientTerminal) status() acp.TerminalExitStatus {
	terminal.mu.Lock()
	defer terminal.mu.Unlock()
	if terminal.exitStatus == nil {
		return acp.TerminalExitStatus{}
	}
	return *cloneTerminalExitStatus(terminal.exitStatus)
}

func terminalOutputLimit(requested *int) (int, error) {
	if requested == nil {
		return defaultTerminalOutputBytes, nil
	}
	if *requested < 0 {
		return 0, errors.New("ACP terminal output byte limit cannot be negative")
	}
	return min(*requested, maxTerminalOutputBytes), nil
}

func terminalEnvironment(overrides []acp.EnvVariable) ([]string, error) {
	environment := os.Environ()
	positions := make(map[string]int, len(environment))
	for index, entry := range environment {
		name, _, _ := strings.Cut(entry, "=")
		positions[name] = index
	}
	for _, variable := range overrides {
		if variable.Name == "" || strings.ContainsAny(variable.Name, "=\x00") {
			return nil, fmt.Errorf("invalid ACP terminal environment variable name %q", variable.Name)
		}
		if strings.ContainsRune(variable.Value, '\x00') {
			return nil, fmt.Errorf(
				"ACP terminal environment variable %q contains a null byte",
				variable.Name,
			)
		}
		entry := variable.Name + "=" + variable.Value
		if index, ok := positions[variable.Name]; ok {
			environment[index] = entry
			continue
		}
		positions[variable.Name] = len(environment)
		environment = append(environment, entry)
	}
	return environment, nil
}

func trimTerminalOutput(output []byte, limit int) []byte {
	if limit == 0 {
		return nil
	}
	start := len(output) - limit
	for start < len(output) && !utf8.RuneStart(output[start]) {
		start++
	}
	return append(output[:0], output[start:]...)
}

func cleanTerminalOutput(output []byte) string {
	output = bytes.ReplaceAll(output, []byte("\r\n"), []byte("\n"))
	output = bytes.ReplaceAll(output, []byte("\r"), []byte("\n"))
	if utf8.Valid(output) {
		return string(output)
	}
	return strings.ToValidUTF8(string(output), "\uFFFD")
}

func cloneTerminalExitStatus(status *acp.TerminalExitStatus) *acp.TerminalExitStatus {
	if status == nil {
		return nil
	}
	cloned := *status
	if status.ExitCode != nil {
		exitCode := *status.ExitCode
		cloned.ExitCode = &exitCode
	}
	if status.Signal != nil {
		signal := *status.Signal
		cloned.Signal = &signal
	}
	return &cloned
}

func terminalNotFound(terminalID string) error {
	return fmt.Errorf("ACP terminal %q was not found", terminalID)
}
