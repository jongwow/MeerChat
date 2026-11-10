package client

import (
	"context"

	"github.com/jongwow/meerchat/internal/chat"
)

// Local adapts a Manager to chat.Backend for in-process use.
type Local struct{ M *Manager }

var _ chat.Backend = Local{}

func (l Local) Join(ctx context.Context, address, password string) (chat.RoomInfo, error) {
	return l.M.Join(ctx, address, password)
}

func (l Local) Leave(ctx context.Context, room string) error { return l.M.Leave(ctx, room) }

func (l Local) Send(ctx context.Context, room, kind, text string) error {
	return l.M.Send(ctx, room, kind, text)
}

func (l Local) Rooms(context.Context) ([]chat.RoomInfo, error) { return l.M.Rooms(), nil }

func (l Local) Nick(context.Context) (string, error) { return l.M.Nick(), nil }

func (l Local) SetNick(ctx context.Context, nick string) error { return l.M.SetNick(ctx, nick) }

func (l Local) Subscribe(ctx context.Context, backlog int) (<-chan chat.Event, error) {
	ch, cancel := l.M.Subscribe(backlog)
	go func() {
		<-ctx.Done()
		cancel()
	}()
	return ch, nil
}

func (l Local) Identity(context.Context) (string, error) { return l.M.Identity(), nil }

func (l Local) Trust(_ context.Context, room, nick string) (string, error) {
	return l.M.Trust(room, nick)
}
