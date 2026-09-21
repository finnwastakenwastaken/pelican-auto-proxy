# Security policy

For the threat model — what the VPS can see, what a leaked token or join code allows, hardening recommendations —
see [docs/security.md](docs/security.md). This page is about reporting a vulnerability, not living with one.

## Reporting a vulnerability

Please do not open a public issue for a security vulnerability. Instead, use GitHub's private vulnerability
reporting for this repository (**Security → Report a vulnerability**), which reaches the maintainer without
disclosing the details publicly first.

Include, if you can:

- What component is affected (VPS agent, node client, plugin, or an installer).
- Steps to reproduce, or a minimal example.
- What you'd expect to happen instead.

There's no bug bounty. You will get a response acknowledging the report, and credit in the fix's changelog entry
unless you'd rather stay anonymous.

## Supported versions

Only the latest released version is supported with security fixes. Given the install method (a versioned release
asset, checksum-verified by the installers), staying current is a matter of re-running the installers described in
[docs/updating.md](docs/updating.md) — there is no long-term support branch to backport a fix to.

| Version | Supported |
|---|---|
| Latest release | Yes |
| Anything older | No — update first |
| Pre-release / development builds | No — for testing only, never for a real deployment |
