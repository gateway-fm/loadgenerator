# Security Policy

## Supported Versions

This project is developed on `main`. Security fixes are applied to `main` and
published in the next tagged release and the corresponding
`gatewayfm/loadgenerator` Docker image. Older tags are not backported.

| Version | Supported |
|---------|-----------|
| `main` and the latest release | Yes |
| Any earlier release | No |

## Reporting a Vulnerability

**Please do not report security vulnerabilities through public GitHub issues,
pull requests, or discussions.**

Report privately through GitHub's private vulnerability reporting:

1. Go to the [Security tab](https://github.com/gateway-fm/loadgenerator/security)
   of this repository.
2. Click **Report a vulnerability**.
3. Describe the issue and how to reproduce it.

The report is visible only to the repository maintainers until an advisory is
published. If you cannot use GitHub advisories, open a regular issue containing
no technical detail — just ask a maintainer to make private contact — and we
will take it from there.

### What to include

The more of this you can provide, the faster we can act:

- The type of issue and the component affected (for example, the HTTP API in
  `internal/transport/`, nonce handling in `internal/account/`, or the SQLite
  persistence layer).
- The affected version, commit SHA, or image tag.
- Step-by-step reproduction instructions, including any configuration or
  environment variables needed.
- Proof-of-concept code, request payloads, or logs, with any credentials
  redacted.
- The impact as you see it, and any suggested mitigation.

### What to expect

- **Acknowledgement** within 3 business days.
- **Initial assessment** — whether we can reproduce it and our severity
  judgement — within 10 business days.
- **Progress updates** at least every 10 business days until the issue is
  resolved or we explain why we consider it out of scope.
- **Credit** in the published advisory and release notes, unless you ask us not
  to.

We ask that you give us a reasonable opportunity to release a fix before any
public disclosure, and that you avoid accessing or modifying data that is not
your own while investigating.

## Scope

This is a **load-testing tool for blockchain sequencers**, intended to be run by
an operator against infrastructure they control, on a trusted network. It is not
designed to be exposed to the public internet, and its HTTP API has no
authentication by design.

**In scope:**

- Remote code execution, command injection, or path traversal reachable through
  the HTTP API or WebSocket endpoints.
- SQL injection or other integrity issues in the SQLite persistence layer.
- Leakage of funded-account private keys through the API, logs, metrics, or the
  database.
- Vulnerabilities in the published `gatewayfm/loadgenerator` container image or
  the release pipeline.
- Dependency vulnerabilities that are actually reachable from this codebase.

**Out of scope:**

- The unauthenticated HTTP API itself. This is a documented design decision; run
  the load generator on a trusted network or behind your own access control.
- Resource exhaustion on a target node caused by the load generator doing its
  job. Generating high transaction throughput is the purpose of this tool.
- Anything requiring a private key or configuration that the operator has
  deliberately supplied.
- Vulnerabilities in the systems under test (sequencers, execution layers, block
  builders) reached through this tool — report those to the relevant project.
- Findings from automated scanners with no demonstrated exploit path.

## Operational Notes

The load generator holds funded account private keys in memory in order to sign
transactions. Use throwaway devnet accounts. Do not point it at an account that
holds anything of value, and do not commit keys to configuration files — see
`.env.example` for the expected environment-variable form.
