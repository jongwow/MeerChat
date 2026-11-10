# Show HN launch kit (draft)

## Title (pick one, ≤ 80 chars)

1. Show HN: Meer – terminal chat where a wrong password just hears meerkats
2. Show HN: Meer – E2E-encrypted terminal chat; the relay can't tell who has the key
3. Show HN: Meer – self-hosted encrypted chat in one Go binary, scriptable from bash

Option 1 is the most memorable; option 2 is the most technical. Link to the
GitHub repo, not the blog post. The README has the screenshot above the fold.

## First comment (post right after submitting)

> Hi HN! meer started as a 2020 toy project with a joke: join a room with the
> wrong password and every message turns into "meer". Back then the server
> did that by checking passwords, so it knew every password and every message.
>
> The rewrite moves the joke to the client. A room is a name and a password.
> Clients route by a hash of the name, so right and wrong passwords meet in
> the same room. They encrypt with a key derived from the password
> (Argon2id → HKDF → XChaCha20-Poly1305), so only the right password decrypts.
> Anything that fails to decrypt is shown as meerkat noise. As a result the
> relay never sees passwords, messages, nicknames or room names, and can't
> even tell which participants have the key.
>
> Other bits: one static binary (relay, TUI, daemon, SSH demo); rooms live in
> a tmux-style daemon so you can `make && meer send done` or
> `meer tail --json | jq`; resumable delivery with an outbox; and SQLite
> history that only ever contains ciphertext.
>
> What it does NOT do, so nobody has to dig: offline password guessing is
> possible for anyone holding a ciphertext (use a passphrase), there is no
> forward secrecy, sender identity is trust-on-first-use (messages are signed
> per device and a nickname that changes keys gets a ⚠), and metadata is
> visible to the relay. Details are in docs/THREAT_MODEL.md. It hasn't been audited, and I'd
> love protocol critique.
>
> Try it: `go install github.com/jongwow/meerchat/cmd/meer@latest`, then
> `meer serve &` and `meer join meer://localhost/lobby`. [Or `ssh -p 2222 <demo host>`.]

## Likely questions, prepared answers

**"Why not Matrix/Signal/IRC/Briar?"**
Use them for real communities. meer is for "encrypted room in 30 seconds, no
accounts, from a terminal, pipe stuff into it". It's closer to `nc` +
`age` than to Slack.

**"Room name as salt? That enables precomputation."**
Yes, for popular room names. The relay must route right and wrong passwords
together without an extra round trip, so the salt must be derivable from what
both sides know. A random per-room salt would need a server-stored room record,
which is exactly the state we avoid. Mitigation: unguessable room names for
sensitive rooms. A PAKE is on the list.

**"Anyone with the password can pretend to be me?"**
Not silently. Every message is signed by a per-device Ed25519 key, and each
client remembers which key a nickname used first in each room. A different key
under a known name is shown with ⚠ until you `/trust` it. The gap is the
usual TOFU one: an impostor who speaks first under a name nobody has seen yet.

**"No forward secrecy?"**
Correct, and it's documented. A single shared password can't give FS without
key agreement between participants (MLS/sender keys). That's the biggest item
on the roadmap.

**"60 ms Argon2 isn't much."**
It's 64 MiB, 3 passes, 4 lanes: the RFC 9106 second recommended profile, scaled
for interactive joins. The real defense is password entropy, and the docs say
so.

**"Can't the relay replay old messages?"**
Exact duplicates within a 4096-message window are dropped by payload ID.
Older replays, withholding and reordering are possible and documented.
Signed or hash-chained sequence numbers are on the list.

**"What stops someone flooding a room?"**
Per-connection rate limits and frame-size limits, caps per room and globally,
and slow consumers get disconnected. Anyone with the password can still be
annoying, but they could be in any chat.

**"Why WebSockets and not raw TCP / QUIC?"**
It works through every proxy, CDN and Caddy config people already have, and
TLS comes for free with `meers://`.

**"Is the meerkat text a side channel?"**
No. It's a deterministic function of the ciphertext the wrong-password client
already holds.

## Checklist before posting

- [ ] Tag `v0.1.0` and let the release workflow publish binaries and the ghcr image
- [ ] Rename the GitHub repo to lowercase `meerchat` (module path is `github.com/jongwow/meerchat`)
- [ ] Record `docs/demo.gif` with `vhs docs/demo.tape` (optional: README already has a PNG)
- [ ] Deploy a public relay + `meer ssh` demo (see docs/DEPLOY.md) and put the host in the README
- [ ] Publish the blog post (docs/launch/blog-meerkat-mode.md) somewhere you own
- [ ] Post on a weekday morning US Pacific time and stick around for a few hours to answer
