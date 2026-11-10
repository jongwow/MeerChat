---
title: "A chat server that can't tell who has the password"
draft: true
---

# A chat server that can't tell who has the password

*Or: how a joke feature from a 2020 side project turned into the whole design.*

In 2020 a couple of us wrote a toy terminal chat in Go called MeerChat. It had
one odd feature. If you joined a room with the wrong password, you weren't
rejected. You got in, but every message, yours and everyone else's, was
replaced with the word `meer`. We called it meerkat mode.

It was implemented the obvious way. The server compared your password with the
room's, and if they didn't match, it rewrote messages on the way out. Which
means the server knew every password and every message. The joke worked; the
privacy didn't.

Coming back to the project years later, I wanted to keep the joke and fix
everything else. The surprise was that the joke *is* the right design, once
you move it to the client.

## Make the server stop knowing things

Start from what the server actually needs. It has to put people who want to
talk to each other in the same place, and pass bytes between them. That's it.

So a room in meer is a name and a password, and the client derives two values
from them:

```
route = SHA-256("meerchat/v1/route" ‖ 0 ‖ room)[:16]
key   = HKDF(Argon2id(password, salt = SHA-256("meerchat/v1/salt" ‖ 0 ‖ room)))
```

The **route** only depends on the room name. The server uses it to group
connections and never sees the name itself. The **key** depends on the
password. The server never sees it at all. Every message is sealed with
XChaCha20-Poly1305 under the key, padded to 128 bytes so lengths leak less,
with the route bound as associated data.

Now replay the wrong-password scenario. You type the right room name and the
wrong password. You land in the same route as everyone else, because the route
doesn't involve the password. Your key is different, so nothing decrypts. The
client has to show *something* for a message it can't authenticate, so it
shows a meerkat:

```
02:35  🐾 meerkat-9af4 │ meeer meer? meer? *sniff*
02:35          mallory │ hello? is this the lobby?
```

Meerkat mode is no longer a server feature. It's what failed decryption looks
like. And it's symmetric: the people with the right password see *you* as a
meerkat, too.

## The nice consequence: the server can't tell either

Because rejection never happens, the server has no way to learn who has the
right password. Everyone sends the same kind of ciphertext and gets the same
kind of ciphertext back. A server that rejected wrong passwords would have to
be able to check them, which means storing something it can test guesses
against. A server that never checks has nothing to leak.

The meerkat noise itself is derived only from the ciphertext: a hash picks
the sounds, and the length picks how many. It carries exactly as much
information as the ciphertext a wrong-password client already holds, which is
no more than the server has.

## What this doesn't solve

I'd rather say this up front than read it in the comments:

- **Offline guessing.** Anyone who holds one ciphertext can test passwords at
  home. That includes the server, an eavesdropper, and anyone who joined with
  the wrong password. Argon2id makes each guess cost about 60 ms and 64 MiB.
  That's a speed bump for `hunter2`, not a wall. Use a passphrase.
- **Room names are guessable.** The server can't invert a route, but it can
  confirm that a route is `SHA-256(... "lobby")`. If the name matters, pick an
  unguessable one.
- **No forward secrecy.** One password, one key: a leaked password unlocks
  recorded history. Per-sender ratchets would fix this, and would make the
  thing several times bigger.
- **Trust on first use.** Messages are signed per device, and a familiar
  nickname showing up with a new key gets a ⚠. But whoever speaks first under
  a name is the one remembered, just as with SSH host keys.

The [threat model](../THREAT_MODEL.md) has the full list.

## The rest of the rewrite

Fixing the crypto made it worth fixing everything else:

- **One binary.** `meer serve` runs a relay, `meer` opens a Bubble Tea UI, and
  `meer ssh` serves that UI over SSH for people who want to try it first.
- **A daemon, like tmux.** Rooms stay connected in the background. The UI
  attaches and detaches, and so can scripts: `make && meer send "done"`,
  `meer tail --json | jq`.
- **Delivery you can reason about.** The relay assigns per-room sequence
  numbers and echoes every message back to its sender. Clients keep an outbox
  until the echo arrives, resend on reconnect, and de-duplicate by payload ID.
  That gives at-least-once on the wire and exactly-once on screen, and it
  survives a relay restart that loses all history.
- **A relay that defends itself.** Rate limits, size limits, and slow consumers
  get disconnected instead of stalling a room. Everything under `-race`, with
  fuzzing on the parsers.
- **Untrusted text is sanitized** before it reaches your terminal, because
  "anyone with the password can send escape sequences to everyone" is a real
  bug class.

The code is at github.com/jongwow/meerchat. I'd especially love critique of
[the protocol](../PROTOCOL.md).
