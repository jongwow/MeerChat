# Contributing

Thanks for taking a look. Bug reports, protocol critique and small focused PRs
are all welcome.

## Setup

```sh
git clone https://github.com/jongwow/meerchat && cd meerchat
go test -race ./...
go run ./cmd/meer serve      # in one terminal
go run ./cmd/meer            # in another
```

Go 1.26 or newer. Nothing else is needed: the SQLite driver is pure Go.

To run several clients on one machine, give each its own daemon socket and
config directory:

```sh
alias as='f(){ MEER_CONFIG_DIR=/tmp/meer-$1/cfg MEER_SOCKET=/tmp/meer-$1/d.sock go run ./cmd/meer "${@:2}"; }; f'
as bob join meer://localhost/lobby
as bob
```

## Guidelines

- Run `go test -race ./...` and `golangci-lint run` before sending a PR.
- Changes to anything in [docs/PROTOCOL.md](docs/PROTOCOL.md) need a matching
  update to that document. Incompatible changes need a new protocol version.
- Security-relevant changes should say what they change in
  [docs/THREAT_MODEL.md](docs/THREAT_MODEL.md).
- Untrusted text must go through `chat.Sanitize` before it reaches a terminal.
- Keep the relay ignorant: if a feature needs the server to learn something
  new, it needs a very good reason and a note in the threat model.

## Layout

See the "Architecture" section of the README. Tests sit next to the code. The
`client`, `daemon` and `sshdemo` packages have end-to-end tests that start a
real relay on a loopback port.
