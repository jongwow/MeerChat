# Threat model

meer is a small tool with a narrow promise. This page states it precisely,
then lists what it does not promise. The protocol details are in
[PROTOCOL.md](PROTOCOL.md).

> **Status:** not audited. The construction uses standard primitives
> (Argon2id, HKDF-SHA256, XChaCha20-Poly1305) composed in the simplest way
> that could work, but nobody independent has reviewed it yet.

## The promise

**Someone who does not know a room's password cannot read its messages,
nicknames or name, even if they run the relay, record the network, or join
the room.**

## Who we consider

| Adversary | Can | Gets |
|---|---|---|
| **Network observer** (no TLS) | read and modify traffic | route IDs, ciphertext, timing, sizes, IPs. Modifications are detected and become meerkats. |
| **Network observer** (TLS, `meers://`) | read traffic metadata | timing, approximate volume, IPs |
| **Relay operator** | everything the relay sees, plus drop, delay, reorder, replay, and inject | see "What the relay learns" below |
| **Wrong-password participant** | join any room whose name they know | the same as the relay sees for that room, plus meerkats |
| **Right-password participant** | read and write | everything in the room, by design |
| **Another local user** on your machine | open sockets, list processes | nothing: the daemon socket is `0600` in a `0700` directory, and passwords never appear in argv |
| **Someone who steals the relay's database** | read it | route IDs, sequence numbers, ephemeral peer IDs, timestamps, ciphertext |

## What the relay learns

- **Route IDs.** A route is a truncated SHA-256 of the room name. The relay
  can't invert it, but it *can* confirm a guess: if you call your room
  `lobby`, the relay can tell. Treat the room name as public unless it is
  itself unguessable.
- **Who talks when, and roughly how much.** IP addresses, connection times,
  message timing, and message size rounded up to 128 bytes.
- **How many connections share a room.** It cannot tell which of them have
  the right password.

It does not learn passwords, keys, plaintext, nicknames, room names, or which
participants can read the room.

## What meer does not protect against

**Weak passwords.** Anyone who holds one ciphertext can test password guesses
offline. That includes the relay operator, a wrong-password participant, and
a non-TLS eavesdropper. Each guess costs one Argon2id evaluation, about 60 ms
and 64 MiB on a modern CPU. That's slow, but not slow enough for `hunter2`.
**Use a passphrase of several random words.** Because the salt is derived from
the room name, an attacker can precompute guesses for popular room names, but
not for all rooms at once.

**Compromised passwords, past and future.** There is no forward secrecy. Every
message in a room is encrypted under one key derived from the password.
Someone who learns the password later can decrypt anything they recorded or
any history the relay still stores. Changing the password means moving to a
new room key, which in practice means telling everyone a new password.

**Impersonation inside the room.** The key proves you know the password, not
who you are. Anyone in the room can send messages under any nickname.
Nicknames are labels, not identities.

**A malicious relay.** It cannot read or forge messages, but it can:

- drop or delay messages, or withhold them from some clients,
- replay old ciphertexts (clients drop exact duplicates they remember, with a
  4096-message window, but older replays would be shown again),
- reorder messages and lie about timestamps (the UI shows relay time),
- disconnect people or report wrong peer counts.

A client cannot currently detect withholding or replays outside its window.

**Metadata.** See "What the relay learns". meer does no cover traffic,
batching or mixing.

**Your endpoint.** The daemon keeps room keys in memory for as long as you are
in the room. Malware running as you can read them. Scrollback lives only in
daemon memory, not on disk. History stays on the relay, encrypted, subject to
its retention setting.

**The SSH demo.** `meer ssh` runs each visitor's chat client on the demo host.
That host sees plaintext for anything typed through it. The UI says so. It
exists so people can try meer without installing it, not for private
conversation.

## Things we do defend against that are easy to miss

- **Terminal injection.** Message text and nicknames are attacker-controlled
  for anyone in the room. Control characters, escape sequences and bidi
  overrides are stripped before anything reaches your terminal, in both the
  UI and `meer tail`.
- **Cross-room replay.** The route is bound as associated data, so a
  ciphertext from room A won't open in room B even with the same password.
- **Malformed but authentic payloads.** A message that decrypts but does not
  validate is treated as noise, not partially trusted.
- **Resource exhaustion at the relay.** Per-connection rate limits, frame size
  limits, per-room and global connection caps, and disconnecting slow
  consumers instead of buffering without bound.
- **Password leaks through the shell.** `meer join` reads passwords from the
  terminal, stdin or an environment variable, never from command-line
  arguments.

## Possible future work

- Password-authenticated key exchange, so guessing requires talking to an
  online participant rather than holding a ciphertext.
- Per-sender keys with signatures (or MLS), for sender authentication and
  forward secrecy.
- Hash-chained or signed sequence numbers, so clients can detect withholding
  and replays.
- Encrypted, padded room names, so the route reveals nothing confirmable.

If you have opinions about any of this, please open an issue.
