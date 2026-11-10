package ipc

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"sync"
	"syscall"
	"time"

	"github.com/jongwow/meerchat/internal/chat"
)

// Client talks to the daemon. It implements chat.Backend.
type Client struct {
	path string

	mu     sync.Mutex
	conn   net.Conn
	r      *bufio.Reader
	nextID uint64
}

var _ chat.Backend = (*Client)(nil)

// Dial connects to the daemon at path.
func Dial(ctx context.Context, path string) (*Client, error) {
	c := &Client{path: path}
	conn, err := c.dial(ctx)
	if err != nil {
		return nil, err
	}
	c.conn, c.r = conn, bufio.NewReaderSize(conn, 64*1024)
	return c, nil
}

func (c *Client) dial(ctx context.Context) (net.Conn, error) {
	var d net.Dialer
	conn, err := d.DialContext(ctx, "unix", c.path)
	if err != nil {
		if _, serr := os.Stat(c.path); errors.Is(serr, os.ErrNotExist) ||
			errors.Is(err, syscall.ENOENT) || errors.Is(err, syscall.ECONNREFUSED) || isRefused(err) {
			return nil, ErrNoDaemon
		}
		return nil, err
	}
	return conn, nil
}

// Close closes the connection.
func (c *Client) Close() error { return c.conn.Close() }

// Call performs one request. result may be nil.
func (c *Client) Call(ctx context.Context, method string, params, result any) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.nextID++
	if err := writeRequest(c.conn, c.nextID, method, params); err != nil {
		return err
	}
	stop := context.AfterFunc(ctx, func() { _ = c.conn.SetDeadline(aLongTimeAgo) })
	defer stop()
	line, err := c.r.ReadBytes('\n')
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return fmt.Errorf("daemon connection: %w", err)
	}
	var resp Response
	if err := json.Unmarshal(line, &resp); err != nil {
		return err
	}
	if resp.Error != "" {
		return errors.New(resp.Error)
	}
	if result != nil && len(resp.Result) > 0 {
		return json.Unmarshal(resp.Result, result)
	}
	return nil
}

func writeRequest(conn net.Conn, id uint64, method string, params any) error {
	req := Request{ID: id, Method: method}
	if params != nil {
		b, err := json.Marshal(params)
		if err != nil {
			return err
		}
		req.Params = b
	}
	b, err := json.Marshal(req)
	if err != nil {
		return err
	}
	_, err = conn.Write(append(b, '\n'))
	return err
}

// Hello returns the daemon's version.
func (c *Client) Hello(ctx context.Context) (HelloResult, error) {
	var r HelloResult
	err := c.Call(ctx, MethodHello, nil, &r)
	return r, err
}

func (c *Client) Join(ctx context.Context, address, password string) (chat.RoomInfo, error) {
	var r chat.RoomInfo
	err := c.Call(ctx, MethodJoin, JoinParams{Address: address, Password: password}, &r)
	return r, err
}

func (c *Client) Leave(ctx context.Context, room string) error {
	return c.Call(ctx, MethodLeave, RoomParams{Room: room}, nil)
}

func (c *Client) Send(ctx context.Context, room, kind, text string) error {
	return c.Call(ctx, MethodSend, SendParams{Room: room, Kind: kind, Text: text}, nil)
}

// SendAndWait sends and waits up to wait for the relay to confirm.
func (c *Client) SendAndWait(ctx context.Context, room, kind, text string, wait time.Duration) error {
	return c.Call(ctx, MethodSend, SendParams{Room: room, Kind: kind, Text: text, WaitMS: wait.Milliseconds()}, nil)
}

func (c *Client) Rooms(ctx context.Context) ([]chat.RoomInfo, error) {
	var r []chat.RoomInfo
	err := c.Call(ctx, MethodRooms, nil, &r)
	return r, err
}

func (c *Client) Nick(ctx context.Context) (string, error) {
	var r NickParams
	err := c.Call(ctx, MethodNick, nil, &r)
	return r.Nick, err
}

func (c *Client) SetNick(ctx context.Context, nick string) error {
	return c.Call(ctx, MethodSetNick, NickParams{Nick: nick}, nil)
}

func (c *Client) Identity(ctx context.Context) (string, error) {
	var r KeyResult
	err := c.Call(ctx, MethodIdentity, nil, &r)
	return r.Key, err
}

func (c *Client) Trust(ctx context.Context, room, nick string) (string, error) {
	var r KeyResult
	err := c.Call(ctx, MethodTrust, TrustParams{Room: room, Nick: nick}, &r)
	return r.Key, err
}

// Shutdown asks the daemon to leave all rooms and exit.
func (c *Client) Shutdown(ctx context.Context) error {
	return c.Call(ctx, MethodShutdown, nil, nil)
}

// Subscribe opens a dedicated connection that streams events.
func (c *Client) Subscribe(ctx context.Context, backlog int) (<-chan chat.Event, error) {
	conn, err := c.dial(ctx)
	if err != nil {
		return nil, err
	}
	if err := writeRequest(conn, 1, MethodSubscribe, SubscribeParams{Backlog: backlog}); err != nil {
		conn.Close()
		return nil, err
	}
	r := bufio.NewReaderSize(conn, 64*1024)
	line, err := r.ReadBytes('\n')
	if err != nil {
		conn.Close()
		return nil, err
	}
	var resp Response
	if err := json.Unmarshal(line, &resp); err != nil || resp.Error != "" {
		conn.Close()
		if err == nil {
			err = errors.New(resp.Error)
		}
		return nil, err
	}
	ch := make(chan chat.Event, 256)
	stop := context.AfterFunc(ctx, func() { conn.Close() })
	go func() {
		defer close(ch)
		defer stop()
		defer conn.Close()
		for {
			line, err := r.ReadBytes('\n')
			if err != nil {
				return
			}
			var it StreamItem
			if json.Unmarshal(line, &it) != nil || it.Event == nil {
				continue
			}
			select {
			case ch <- *it.Event:
			case <-ctx.Done():
				return
			}
		}
	}()
	return ch, nil
}

// aLongTimeAgo is a deadline in the past, used to unblock a pending read.
var aLongTimeAgo = time.Unix(1, 0)
