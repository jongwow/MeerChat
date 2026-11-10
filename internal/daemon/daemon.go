// Package daemon keeps rooms connected in the background, like tmux keeps
// shells running. Front ends (`meer`, `meer send`, `meer tail`, scripts)
// attach and detach over a Unix socket.
package daemon

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"os"
	"sync"
	"time"

	"github.com/jongwow/meerchat/internal/client"
	"github.com/jongwow/meerchat/internal/ipc"
	"github.com/jongwow/meerchat/internal/version"
)

// Options configure the daemon.
type Options struct {
	Socket  string
	Manager client.Options
	Logger  *slog.Logger
	// OnNick is called after the nickname changes, so it can be persisted.
	OnNick func(string)
}

// Run serves until ctx is cancelled or a client calls shutdown.
func Run(ctx context.Context, opts Options) error {
	log := opts.Logger
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	if err := ipc.PrepareSocketDir(opts.Socket); err != nil {
		return err
	}
	ln, err := listen(ctx, opts.Socket)
	if err != nil {
		return err
	}
	defer os.Remove(opts.Socket)
	defer ln.Close()

	opts.Manager.Logger = log
	m, err := client.NewManager(opts.Manager)
	if err != nil {
		return err
	}
	defer m.Close()

	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	d := &daemon{m: m, log: log, shutdown: cancel, onNick: opts.OnNick}

	go func() {
		<-ctx.Done()
		ln.Close()
	}()
	log.Info("daemon listening", "socket", opts.Socket, "version", version.Version())

	var wg sync.WaitGroup
	for {
		conn, err := ln.Accept()
		if err != nil {
			cancel()
			wg.Wait()
			if errors.Is(err, net.ErrClosed) {
				return nil
			}
			return err
		}
		wg.Go(func() { d.serve(ctx, conn) })
	}
}

// listen binds the socket, cleaning up a stale one left by a crash but
// refusing to steal it from a live daemon.
func listen(ctx context.Context, path string) (net.Listener, error) {
	ln, err := net.Listen("unix", path)
	if err == nil {
		return ln, os.Chmod(path, 0o600)
	}
	if c, derr := ipc.Dial(ctx, path); derr == nil {
		c.Close()
		return nil, fmt.Errorf("a daemon is already running on %s", path)
	}
	if rerr := os.Remove(path); rerr != nil {
		return nil, err
	}
	ln, err = net.Listen("unix", path)
	if err != nil {
		return nil, err
	}
	return ln, os.Chmod(path, 0o600)
}

type daemon struct {
	m        *client.Manager
	log      *slog.Logger
	shutdown context.CancelFunc
	onNick   func(string)
}

func (d *daemon) serve(ctx context.Context, conn net.Conn) {
	defer conn.Close()
	stop := context.AfterFunc(ctx, func() { conn.Close() })
	defer stop()

	r := bufio.NewReaderSize(conn, 64*1024)
	enc := json.NewEncoder(conn)
	for {
		line, err := r.ReadBytes('\n')
		if err != nil {
			return
		}
		var req ipc.Request
		if err := json.Unmarshal(line, &req); err != nil {
			_ = enc.Encode(ipc.Response{Error: "bad request: " + err.Error()})
			return
		}
		if req.Method == ipc.MethodSubscribe {
			d.stream(ctx, conn, enc, req)
			return
		}
		result, err := d.call(ctx, req)
		resp := ipc.Response{ID: req.ID}
		if err != nil {
			resp.Error = err.Error()
		} else if result != nil {
			resp.Result, _ = json.Marshal(result)
		}
		if err := enc.Encode(resp); err != nil {
			return
		}
	}
}

func (d *daemon) call(ctx context.Context, req ipc.Request) (any, error) {
	decode := func(v any) error {
		if len(req.Params) == 0 {
			return nil
		}
		return json.Unmarshal(req.Params, v)
	}
	// Joins run Argon2 and dial the network; give them room, but not forever.
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	switch req.Method {
	case ipc.MethodHello:
		return ipc.HelloResult{Version: version.Version(), PID: os.Getpid()}, nil
	case ipc.MethodJoin:
		var p ipc.JoinParams
		if err := decode(&p); err != nil {
			return nil, err
		}
		return d.m.Join(ctx, p.Address, p.Password)
	case ipc.MethodLeave:
		var p ipc.RoomParams
		if err := decode(&p); err != nil {
			return nil, err
		}
		return nil, d.m.Leave(ctx, p.Room)
	case ipc.MethodSend:
		var p ipc.SendParams
		if err := decode(&p); err != nil {
			return nil, err
		}
		return nil, d.m.Send(ctx, p.Room, p.Kind, p.Text)
	case ipc.MethodRooms:
		return d.m.Rooms(), nil
	case ipc.MethodNick:
		return ipc.NickParams{Nick: d.m.Nick()}, nil
	case ipc.MethodSetNick:
		var p ipc.NickParams
		if err := decode(&p); err != nil {
			return nil, err
		}
		if err := d.m.SetNick(ctx, p.Nick); err != nil {
			return nil, err
		}
		if d.onNick != nil {
			d.onNick(p.Nick)
		}
		return nil, nil
	case ipc.MethodShutdown:
		d.log.Info("shutdown requested")
		// Answer first, then stop.
		time.AfterFunc(50*time.Millisecond, d.shutdown)
		return nil, nil
	}
	return nil, errors.New("unknown method " + req.Method)
}

func (d *daemon) stream(ctx context.Context, conn net.Conn, enc *json.Encoder, req ipc.Request) {
	var p ipc.SubscribeParams
	if len(req.Params) > 0 {
		_ = json.Unmarshal(req.Params, &p)
	}
	ch, cancel := d.m.Subscribe(p.Backlog)
	defer cancel()
	if err := enc.Encode(ipc.Response{ID: req.ID}); err != nil {
		return
	}
	// Notice the peer hanging up even though we never read from it again.
	gone := make(chan struct{})
	go func() {
		_, _ = conn.Read(make([]byte, 1))
		close(gone)
	}()
	for {
		select {
		case <-ctx.Done():
			return
		case <-gone:
			return
		case e, ok := <-ch:
			if !ok {
				return // fell behind; client resubscribes
			}
			_ = conn.SetWriteDeadline(time.Now().Add(5 * time.Second))
			if err := enc.Encode(ipc.StreamItem{Event: &e}); err != nil {
				return
			}
		}
	}
}
