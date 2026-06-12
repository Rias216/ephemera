package mcp

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"sync"
)

type stdioResult struct {
	response rpcResponse
	err      error
}

type stdioTransport struct {
	cmd       *exec.Cmd
	stdin     io.WriteCloser
	writeMu   sync.Mutex
	pendingMu sync.Mutex
	pending   map[int64]chan stdioResult
	closedErr error
	closeOnce sync.Once
}

func newStdioTransport(command string, args []string, cwd string, env map[string]string) (*stdioTransport, error) {
	command = strings.TrimSpace(command)
	if command == "" {
		return nil, fmt.Errorf("stdio MCP command is required")
	}
	cmd := exec.Command(command, args...)
	cmd.Dir = cwd
	cmd.Env = append([]string(nil), os.Environ()...)
	for key, value := range env {
		cmd.Env = append(cmd.Env, key+"="+os.ExpandEnv(value))
	}
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	transport := &stdioTransport{cmd: cmd, stdin: stdin, pending: map[int64]chan stdioResult{}}
	go transport.readLoop(stdout)
	go io.Copy(io.Discard, stderr)
	return transport, nil
}

func (t *stdioTransport) readLoop(reader io.Reader) {
	scanner := bufio.NewScanner(reader)
	scanner.Buffer(make([]byte, 64*1024), 8<<20)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		var response rpcResponse
		if err := json.Unmarshal([]byte(line), &response); err != nil {
			t.fail(fmt.Errorf("decode MCP stdio response: %w", err))
			return
		}
		if response.ID == 0 && response.Method != "" {
			continue
		}
		t.pendingMu.Lock()
		ch := t.pending[response.ID]
		if ch != nil {
			delete(t.pending, response.ID)
		}
		t.pendingMu.Unlock()
		if ch != nil {
			ch <- stdioResult{response: response}
			close(ch)
		}
	}
	if err := scanner.Err(); err != nil {
		t.fail(err)
	} else {
		t.fail(io.EOF)
	}
}

func (t *stdioTransport) fail(err error) {
	t.pendingMu.Lock()
	if t.closedErr == nil {
		t.closedErr = err
	}
	pending := t.pending
	t.pending = map[int64]chan stdioResult{}
	t.pendingMu.Unlock()
	for _, ch := range pending {
		ch <- stdioResult{err: err}
		close(ch)
	}
}

func (t *stdioTransport) Send(ctx context.Context, request rpcRequest, expect bool) (rpcResponse, error) {
	data, err := json.Marshal(request)
	if err != nil {
		return rpcResponse{}, err
	}
	var responseCh chan stdioResult
	if expect {
		responseCh = make(chan stdioResult, 1)
		t.pendingMu.Lock()
		if t.closedErr != nil {
			err := t.closedErr
			t.pendingMu.Unlock()
			return rpcResponse{}, err
		}
		t.pending[request.ID] = responseCh
		t.pendingMu.Unlock()
	}
	t.writeMu.Lock()
	_, err = t.stdin.Write(append(data, '\n'))
	t.writeMu.Unlock()
	if err != nil {
		if expect {
			t.pendingMu.Lock()
			delete(t.pending, request.ID)
			t.pendingMu.Unlock()
		}
		return rpcResponse{}, err
	}
	if !expect {
		return rpcResponse{}, nil
	}
	select {
	case <-ctx.Done():
		t.pendingMu.Lock()
		delete(t.pending, request.ID)
		t.pendingMu.Unlock()
		return rpcResponse{}, ctx.Err()
	case result := <-responseCh:
		return result.response, result.err
	}
}

func (t *stdioTransport) SetProtocolVersion(string) {}

func (t *stdioTransport) Close() error {
	var err error
	t.closeOnce.Do(func() {
		_ = t.stdin.Close()
		if t.cmd.Process != nil {
			_ = t.cmd.Process.Kill()
		}
		err = t.cmd.Wait()
		t.fail(io.EOF)
	})
	return err
}
