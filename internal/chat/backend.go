package chat

import "context"

// Backend is what a front end (TUI, CLI) needs from a chat engine. The daemon
// client implements it over a socket; the SSH demo implements it in-process.
type Backend interface {
	Join(ctx context.Context, address, password string) (RoomInfo, error)
	Leave(ctx context.Context, room string) error
	// Send posts text to room ("" means the most recently joined room).
	// kind is KindMessage or KindAction.
	Send(ctx context.Context, room, kind, text string) error
	Rooms(ctx context.Context) ([]RoomInfo, error)
	Nick(ctx context.Context) (string, error)
	SetNick(ctx context.Context, nick string) error
	// Subscribe streams recent history (up to backlog events per room) and
	// then live events. The channel closes when ctx is done or the
	// subscriber fell behind and must resubscribe.
	Subscribe(ctx context.Context, backlog int) (<-chan Event, error)
	// Identity returns this device's key fingerprint.
	Identity(ctx context.Context) (string, error)
	// Trust accepts the key nick most recently used in room, after it was
	// flagged as changed. It returns the newly trusted fingerprint.
	Trust(ctx context.Context, room, nick string) (string, error)
}
