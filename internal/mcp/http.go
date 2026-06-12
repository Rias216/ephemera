package mcp

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"
)

type httpTransport struct {
	url             string
	headers         map[string]string
	client          *http.Client
	mu              sync.RWMutex
	sessionID       string
	protocolVersion string
}

func newHTTPTransport(url string, headers map[string]string, timeout time.Duration) (*httpTransport, error) {
	if strings.TrimSpace(url) == "" {
		return nil, fmt.Errorf("Streamable HTTP MCP URL is required")
	}
	copyHeaders := make(map[string]string, len(headers))
	for key, value := range headers {
		copyHeaders[key] = osExpand(value)
	}
	return &httpTransport{url: url, headers: copyHeaders, client: &http.Client{Timeout: timeout}}, nil
}

func (t *httpTransport) snapshot() (string, string) {
	t.mu.RLock()
	defer t.mu.RUnlock()
	return t.sessionID, t.protocolVersion
}

func (t *httpTransport) Send(ctx context.Context, request rpcRequest, expect bool) (rpcResponse, error) {
	data, err := json.Marshal(request)
	if err != nil {
		return rpcResponse{}, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, t.url, bytes.NewReader(data))
	if err != nil {
		return rpcResponse{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	for key, value := range t.headers {
		req.Header.Set(key, value)
	}
	session, version := t.snapshot()
	if session != "" {
		req.Header.Set("MCP-Session-Id", session)
	}
	if version != "" {
		req.Header.Set("MCP-Protocol-Version", version)
	}
	response, err := t.client.Do(req)
	if err != nil {
		return rpcResponse{}, err
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(response.Body, 8*1024))
		return rpcResponse{}, fmt.Errorf("MCP HTTP %d: %s", response.StatusCode, strings.TrimSpace(string(body)))
	}
	if id := strings.TrimSpace(response.Header.Get("MCP-Session-Id")); id != "" {
		t.mu.Lock()
		t.sessionID = id
		t.mu.Unlock()
	}
	if !expect || response.StatusCode == http.StatusAccepted || response.StatusCode == http.StatusNoContent {
		return rpcResponse{}, nil
	}
	contentType := strings.ToLower(response.Header.Get("Content-Type"))
	if strings.Contains(contentType, "text/event-stream") {
		return readSSEResponse(response.Body, request.ID)
	}
	var decoded rpcResponse
	if err := json.NewDecoder(response.Body).Decode(&decoded); err != nil {
		return rpcResponse{}, err
	}
	return decoded, nil
}

func readSSEResponse(reader io.Reader, id int64) (rpcResponse, error) {
	scanner := bufio.NewScanner(reader)
	scanner.Buffer(make([]byte, 64*1024), 8<<20)
	var data strings.Builder
	flush := func() (rpcResponse, bool, error) {
		if data.Len() == 0 {
			return rpcResponse{}, false, nil
		}
		var response rpcResponse
		err := json.Unmarshal([]byte(data.String()), &response)
		data.Reset()
		if err != nil {
			return rpcResponse{}, false, err
		}
		if response.ID == id {
			return response, true, nil
		}
		return rpcResponse{}, false, nil
	}
	for scanner.Scan() {
		line := scanner.Text()
		if line == "" {
			if response, ok, err := flush(); ok || err != nil {
				return response, err
			}
			continue
		}
		if strings.HasPrefix(line, "data:") {
			if data.Len() > 0 {
				data.WriteByte('\n')
			}
			data.WriteString(strings.TrimSpace(strings.TrimPrefix(line, "data:")))
		}
	}
	if response, ok, err := flush(); ok || err != nil {
		return response, err
	}
	if err := scanner.Err(); err != nil {
		return rpcResponse{}, err
	}
	return rpcResponse{}, fmt.Errorf("MCP SSE stream ended without response id %d", id)
}

func (t *httpTransport) SetProtocolVersion(version string) {
	t.mu.Lock()
	t.protocolVersion = version
	t.mu.Unlock()
}

func (t *httpTransport) Close() error {
	session, version := t.snapshot()
	if session == "" {
		return nil
	}
	req, err := http.NewRequest(http.MethodDelete, t.url, nil)
	if err != nil {
		return err
	}
	req.Header.Set("MCP-Session-Id", session)
	if version != "" {
		req.Header.Set("MCP-Protocol-Version", version)
	}
	for key, value := range t.headers {
		req.Header.Set(key, value)
	}
	response, err := t.client.Do(req)
	if err != nil {
		return err
	}
	response.Body.Close()
	return nil
}

func osExpand(value string) string {
	return os.ExpandEnv(value)
}
