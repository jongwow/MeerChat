# Deploying a relay

A relay is one process with no state beyond an optional SQLite file. It holds
only ciphertext, so the bar for "safe to host" is low. It still learns
metadata; see [THREAT_MODEL.md](THREAT_MODEL.md).

## systemd

```ini
# /etc/systemd/system/meer.service
[Unit]
Description=meer relay
After=network-online.target
Wants=network-online.target

[Service]
ExecStart=/usr/local/bin/meer serve --addr 127.0.0.1:6337 --db /var/lib/meer/relay.db --retention 168h
DynamicUser=yes
StateDirectory=meer
Restart=on-failure
NoNewPrivileges=yes
ProtectSystem=strict
ProtectHome=yes
PrivateTmp=yes
PrivateDevices=yes
RestrictAddressFamilies=AF_INET AF_INET6
SystemCallFilter=@system-service
MemoryMax=512M

[Install]
WantedBy=multi-user.target
```

```sh
sudo systemctl enable --now meer
```

## TLS with Caddy

Caddy gets certificates automatically and proxies WebSockets with no special
configuration:

```caddyfile
chat.example.com {
	reverse_proxy 127.0.0.1:6337
}
```

Clients then use `meers://chat.example.com/room`.

Messages are already end-to-end encrypted, so TLS isn't required for
confidentiality. It does hide route IDs and per-message timing from people on
the network path, and it's free, so use it.

## Docker

```sh
docker run -d --name meer -p 6337:6337 -v meer:/data \
  ghcr.io/jongwow/meerchat serve --db /data/relay.db
```

## Tuning

| flag | default | |
|---|---|---|
| `--history` | 500 | messages kept per room; `0` keeps no history at all |
| `--retention` | 168h | messages older than this are deleted; `0` keeps them |
| `--max-conns` | 10000 | total connections |
| `--max-peers` | 512 | connections per room |
| `--allow-browsers` | off | accept WebSocket connections from any web origin |

`GET /healthz` returns `ok`. `GET /v1/stats` returns rooms, connections and
messages relayed as JSON.

## The SSH demo

To let people try meer with nothing but `ssh`, run a relay and the demo next
to it:

```sh
meer serve --addr 127.0.0.1:6337 &
meer ssh --addr :2222 --relay 127.0.0.1:6337 --lobby lobby --lobby-password meer
```

Visitors land in `#lobby` with a nickname taken from their SSH user name, and
can `/join` other rooms **on that relay only**. Their chat client runs on your
server, so your server sees their plaintext; the UI tells them. Don't run the
demo on a relay you use for private rooms you care about. Not because the
relay learns anything new, but because demo visitors' messages pass through
the same machine.
