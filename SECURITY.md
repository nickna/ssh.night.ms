# Security policy

## Reporting a vulnerability

If you find a security issue in this project, please report it privately:

- **Email:** `nick@night.ms`
- **GitHub:** open a private security advisory via the repository's
  "Security" tab → "Report a vulnerability"

Please do not open public GitHub issues for security problems.

When reporting, include:

- A description of the issue and its impact.
- Steps to reproduce, or a proof-of-concept.
- The commit SHA or release tag you tested against.
- Any suggested mitigation, if you have one in mind.

I'll acknowledge receipt within a few days and aim to ship a fix or
mitigation as quickly as the issue's severity warrants. Coordinated
disclosure is welcome; credit is offered on request.

## Scope

In scope:

- The SSH listener (`internal/transport`, `internal/security/netlimit`)
- Auth and rate limiting (`internal/auth`)
- The HTTP / WebSocket surface (`internal/web`)
- The Carbonyl rich-mode browser launcher (`internal/carbonyl`)
- Persistence and migrations (`internal/data`)
- Anything that lets an unauthenticated visitor gain code execution,
  read or modify another user's data, or exhaust shared resources.

Out of scope (but still nice to hear about):

- Issues in upstream dependencies, including Chromium / Carbonyl itself —
  please report those to the upstream project. If the issue is exposed
  through how this project uses the dependency, that is in scope.
- Self-service denial of service that only affects the reporter's own
  account or connection.

## Hardening overview

The `Security` section of [`CLAUDE.md`](./CLAUDE.md) documents the
three-layer defense (protocol limits, network rate-limiting, auth
lockouts + persistent bans), the audit-log pipeline, and the Carbonyl URL
policy. That document is the most thorough description of what is and
isn't already defended against.
