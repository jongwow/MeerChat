# Security policy

Please report vulnerabilities privately through GitHub's
[private vulnerability reporting](https://github.com/jongwow/meerchat/security/advisories/new)
rather than in a public issue.

Include what you found, how to reproduce it, and what you think the impact is.
You should get an acknowledgement within a few days. Once a fix is released,
we're happy to credit you in the advisory.

Before reporting, check [docs/THREAT_MODEL.md](docs/THREAT_MODEL.md): several
limitations (offline password guessing, no forward secrecy, no sender
authentication, metadata visible to the relay) are documented and known. New
ideas for addressing them are still welcome as regular issues.
