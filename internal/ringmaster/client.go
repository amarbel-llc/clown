package ringmaster

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"sync"
	"sync/atomic"
	"time"
)

// Client is a thread-safe JSON-RPC 2.0 client over a Unix domain
// socket. One Client owns one connection; concurrent calls share the
// connection and serialize requests behind a mutex. For high-fanout
// use cases, callers can construct multiple Clients.
type Client struct {
	conn   net.Conn
	br     *bufio.Reader
	mu     sync.Mutex
	nextID atomic.Int64
}

func NewClient(socketPath string) (*Client, error) {
	conn, err := net.Dial("unix", socketPath)
	if err != nil {
		return nil, fmt.Errorf("dial %s: %w", socketPath, err)
	}
	return &Client{conn: conn, br: bufio.NewReader(conn)}, nil
}

func (c *Client) Close() error { return c.conn.Close() }

func (c *Client) call(ctx context.Context, method string, params, result any) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	if deadline, ok := ctx.Deadline(); ok {
		_ = c.conn.SetDeadline(deadline)
		defer c.conn.SetDeadline(time.Time{})
	}

	id := json.Number(fmt.Sprintf("%d", c.nextID.Add(1)))

	var rawParams json.RawMessage
	if params != nil {
		b, err := json.Marshal(params)
		if err != nil {
			return fmt.Errorf("marshal params: %w", err)
		}
		rawParams = b
	}

	if err := WriteFrame(c.conn, Envelope{
		JSONRPC: "2.0",
		ID:      id,
		Method:  method,
		Params:  rawParams,
	}); err != nil {
		return err
	}

	env, err := ReadFrame(c.br)
	if err != nil {
		return err
	}
	if string(env.ID) != string(id) {
		return fmt.Errorf("rpc %s: response id mismatch (sent %s, got %s)", method, id, env.ID)
	}
	if env.Error != nil {
		return fmt.Errorf("rpc %s: %s (code %d)", method, env.Error.Message, env.Error.Code)
	}
	if result == nil {
		return nil
	}
	if err := json.Unmarshal(env.Result, result); err != nil {
		return fmt.Errorf("unmarshal result: %w", err)
	}
	return nil
}

// Generated-style wrappers — one per method. Hand-written for v1.

func (c *Client) StartInstance(ctx context.Context, p StartInstanceParams) (StartInstanceResult, error) {
	var r StartInstanceResult
	return r, c.call(ctx, MethodStartInstance, p, &r)
}

func (c *Client) StopInstance(ctx context.Context, p StopInstanceParams) error {
	return c.call(ctx, MethodStopInstance, p, nil)
}

func (c *Client) StopAll(ctx context.Context) (StopAllResult, error) {
	var r StopAllResult
	return r, c.call(ctx, MethodStopAll, StopAllParams{}, &r)
}

func (c *Client) ListInstances(ctx context.Context) (ListInstancesResult, error) {
	var r ListInstancesResult
	return r, c.call(ctx, MethodListInstances, ListInstancesParams{}, &r)
}

func (c *Client) GetInstance(ctx context.Context, p GetInstanceParams) (GetInstanceResult, error) {
	var r GetInstanceResult
	return r, c.call(ctx, MethodGetInstance, p, &r)
}

func (c *Client) ListAvailableModels(ctx context.Context) (ListAvailableModelsResult, error) {
	var r ListAvailableModelsResult
	return r, c.call(ctx, MethodListAvailableModels, ListAvailableModelsParams{}, &r)
}

func (c *Client) DownloadModel(ctx context.Context, p DownloadModelParams) (DownloadModelResult, error) {
	var r DownloadModelResult
	return r, c.call(ctx, MethodDownloadModel, p, &r)
}
