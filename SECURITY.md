# Security policy

## Reporting a vulnerability

Please **do not** open a public issue for security problems.

Report them privately through GitHub:
**Security → Report a vulnerability** on this repository
(private vulnerability reporting).

Include the affected version, the backend (Kapsule or Kosmos), and the
steps to reproduce. You should get a first answer within a week.

## Supported versions

Only the latest released version receives security fixes.

## Known limitations

The Kosmos backend is experimental and writes the node registration key in
clear text into the instance cloud-init. This is documented in the
[README](README.md) and is not considered a vulnerability report on its own.
