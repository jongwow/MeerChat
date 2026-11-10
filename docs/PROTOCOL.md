# meer protocol, version 1

This document specifies what a meer client and relay say to each other. The
reference implementation is in `internal/crypt`, `internal/protocol`,
`internal/server` and `internal/client`. Where this document and the code
disagree, that is a bug; please report it.

Notation: `‖` is concatenation, `"…"` is the UTF-8 bytes of a string, `0x00`
is a single zero byte, `u32be(n)` is `n` as 4 big-endian bytes.

## 1. Rooms, routes and keys

A room is identified by a **relay address** and a **room name**. It is
protected by a **password**. Room names are 1–64 bytes of UTF-8 without
leading/trailing spaces and without `/ ? # NUL`.

Addresses are written `meer://host[:port]/room` (WebSocket, default port 6337)
or `meers://host[:port]/room` (WebSocket over TLS, default port 443).

### 1.1 Route ID

The relay groups connections by an opaque route ID:

```
route = hex( SHA-256( "meerchat/v1/route" ‖ 0x00 ‖ room )[0:16] )
```

That is 32 lowercase hex characters. The route depends only on the room name,
so clients with different passwords meet in the same room. That is what makes
meerkat mode possible.

### 1.2 Key derivation

```
salt   = SHA-256( "meerchat/v1/salt" ‖ 0x00 ‖ room )
master = Argon2id( password, salt, t=3, m=65536 KiB, p=4, len=32 )
key    = HKDF-SHA256( ikm=master, salt=∅, info="meerchat/v1/message-key", L=32 )
fp     = HKDF-SHA256( ikm=master, salt=∅, info="meerchat/v1/fingerprint", L=4 )
```

The fingerprint is displayed as `XXXX-XXXX` (uppercase hex of `fp[0:2]`, `-`,
`fp[2:4]`). Clients with the same password see the same fingerprint. It is a
sanity check, not authentication.

### 1.3 Sealing

```
padded = u32be(len(p)) ‖ p ‖ zeros       // total length rounded up to a multiple of 128
nonce  = 24 random bytes
aad    = "meerchat/v1/message" ‖ 0x00 ‖ route
ct     = nonce ‖ XChaCha20-Poly1305-Seal( key, nonce, padded, aad )
```

`p` is the JSON envelope of §3, at most 12288 bytes. To open, a client verifies
the tag, checks that `len(padded)` is a multiple of 128 and that the length
prefix fits, and returns `p`. Any failure, for any reason, is a meerkat (§4.3).

Binding the route as associated data stops a relay from moving a ciphertext
between rooms that happen to share a password.

## 2. Transport

```
GET /v1/rooms/{route}/ws?since={seq}
Sec-WebSocket-Protocol: meer.v1
```

- The relay rejects a malformed route with HTTP 400, and closes with 1008
  (policy violation) if `meer.v1` was not negotiated.
- `since` is the last sequence number the client has seen in this room (0 or
  absent if none).
- Every message is a WebSocket **text** frame holding one JSON object. Binary
  data inside JSON is standard base64 with padding.
- Frames are at most 22,869 bytes; ciphertexts are at most 16,384 bytes.
- Either side may send WebSocket pings. The reference relay pings every 25 s,
  the client every 30 s.

### 2.1 Client → relay

| field | type | |
|---|---|---|
| `t` | `"send"` | |
| `ct` | base64 | sealed payload, 1–16384 bytes |

### 2.2 Relay → client

Every frame has `t`. Other fields depend on it:

| `t` | fields | meaning |
|---|---|---|
| `welcome` | `you`, `head`, `peers` | Always the first frame. `you` is this connection's random 8-hex peer ID; `head` is the room's latest sequence number; `peers` counts connections including this one. |
| `msg` | `seq`, `peer`, `ts`, `ct` | A ciphertext. `seq` increases by 1 per message per room; `peer` is the sender's peer ID; `ts` is the relay's receive time in Unix milliseconds. |
| `presence` | `ev` (`join`/`leave`), `peer`, `peers` | A connection arrived or left. |
| `error` | `code`, `msg` | `bad_frame`, `rate_limited` or `internal`. The connection stays open. |

### 2.3 Relay behaviour

1. On connect, the relay sends `welcome`. If `since < head`, it then sends up
   to 200 stored messages with `seq > since`, oldest first, then live
   messages. No message can fall between the replay and live delivery.
2. If `since > head`, the client has seen sequence numbers this relay never
   issued (it restarted without its database). The relay treats `since` as 0
   and replays everything it has.
3. Every accepted `send` is stamped, stored, and fanned out to **every**
   connection in the room, including the sender. The echo is the sender's
   delivery acknowledgement.
4. A connection whose outbound queue overflows is closed with 1008 "too
   slow" rather than allowed to stall the room.
5. Messages beyond the rate limit (reference: 5/s sustained, burst 20) are
   answered with `error rate_limited` and dropped.
6. When shutting down, the relay closes connections with 1001 (going away).
7. Peer IDs are per connection and carry no identity. Sequence numbers are per
   route and never decrease for the lifetime of the relay's store.

## 3. Envelope and payload

### 3.1 Device keys and the envelope

Every client device has an Ed25519 key pair (the reference client keeps it
in its config directory). The plaintext inside every ciphertext is a signed
envelope:

| field | type | |
|---|---|---|
| `b` | base64 | the JSON payload of §3.2 |
| `pk` | base64 | the sender's 32-byte Ed25519 public key |
| `sig` | base64 | `Ed25519-Sign(sk, "meerchat/v1/signed-payload" ‖ 0x00 ‖ route ‖ 0x00 ‖ b)` |

The room key proves the sender knows the password. The signature says which
device sent it. Binding the route stops a signed payload from being replayed
into another room. An envelope whose signature does not verify is noise
(§4.3), exactly like one that does not decrypt.

A device key's **fingerprint** is the first 8 bytes of `SHA-256(pk)` as
lowercase hex in four dash-separated groups, e.g. `ad69-c219-e143-b9e6`.

### 3.2 Payload

| field | type | |
|---|---|---|
| `k` | string | `msg`, `me` (action), `join`, `leave`, `nick` |
| `id` | string | random 128-bit hex, unique per payload |
| `n` | string | sender's nickname: 1–32 bytes, no spaces or control characters |
| `x` | string | message text (≤ 4000 bytes); for `nick`, the *previous* nickname |
| `t` | number | sender's clock, Unix milliseconds (informational) |

A payload that decrypts but fails validation is treated exactly like one that
does not decrypt. Receivers must not render `x` or `n` to a terminal without
removing control and bidi-format characters.

### 3.3 Trust on first use

Receivers remember, per room, the first device key seen for each nickname,
and label every message:

| label | meaning |
|---|---|
| `self` | signed by this device |
| `new` | first key ever seen for this nickname in this room; it is now remembered |
| `known` | the remembered key |
| `changed` | a different key than the remembered one; shown with a warning, and **not** remembered |

A `nick` payload signed by the key remembered for the old nickname carries
that key over to the new one. A user who has confirmed a key change out of
band can accept the most recent key for a nickname (`/trust`, `meer trust`).

## 4. Client behaviour

### 4.1 Ordering and de-duplication

Clients track the highest `seq` seen and ignore `msg` frames at or below it.
They also remember recent payload IDs (reference: 4096) and drop repeats. If
`welcome.head` is lower than the highest `seq` seen, the relay lost history.
The client then resets its cursor to 0 and relies on payload IDs.

### 4.2 Delivery

A client keeps each `msg`, `me` and `nick` payload in an outbox until the
relay echoes it back, and resends whatever is still there after reconnecting
(reference: up to 256 entries, 10 minutes). Delivery is at-least-once on the
wire and exactly-once on screen. `join` is sent once per room per client
session; `leave` is sent best-effort when leaving.

### 4.3 Meerkats

A `msg` frame that fails to open is displayed as coming from
`meerkat-` + the first 4 characters of its peer ID, with text drawn
deterministically from a fixed list of sounds by `SHA-256(ct)`. There are
`len(ct)/48 + 1` words, capped at 12. This reveals nothing beyond the
ciphertext the client already holds.

### 4.4 Reconnecting

Clients reconnect with exponential backoff and full jitter: the wait is a
random duration in [0, b) plus 500 ms, where b starts at 500 ms and doubles
to a cap of 30 s, resetting after a connection lasts longer than 30 s.

## 5. Versioning

Any incompatible change to sections 1–3 gets a new WebSocket subprotocol
(`meer.v2`) and new labels (`meerchat/v2/…`). Relays may serve several
versions at once.
