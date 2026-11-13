# Contributing

Thanks for your interest in karpenter-provider-scaleway!

## Issue first, then pull request

This project does not accept unsolicited pull requests. Every change starts
with an issue:

1. **Open an issue** describing the bug or the feature (use the templates).
   For a feature, explain the use case before the implementation.
2. **Wait for the maintainer to accept it.** An accepted issue gets the
   `accepted` label. The discussion on the issue is where the approach is
   agreed on.
3. **Open a pull request** that links the accepted issue in its description
   with a closing keyword, e.g. `Closes #42`.

Pull requests from external contributors that do not link an issue labelled
`accepted` are closed automatically. Nothing is lost: once the issue is
accepted, add `Closes #<issue>` to the description and reopen the pull
request (or open a new one).

Security issues must **not** be reported in a public issue, see
[SECURITY.md](SECURITY.md).

## Development

Requirements: Go (see `GO_VERSION` in `.github/workflows/ci.yml`), Docker,
Helm, `make`.

Run the same checks as the CI before pushing:

```sh
make ci     # lint + unit tests (race) + envtest
make build
```

Regenerated files (CRD, instance type catalog) must be committed together
with the change that produced them:

```sh
make generate-crd
make generate-catalog   # needs Scaleway credentials
```

## Pull request guidelines

- One logical change per pull request; keep it small enough to review.
- Add or update tests for any behaviour change.
- Update the documentation (`README.md`, `docs/`) and `CHANGELOG.md`
  (`Unreleased` section) when user-visible behaviour changes.
- Commit messages follow [Conventional Commits](https://www.conventionalcommits.org/)
  (`feat:`, `fix:`, `docs:`, `chore:` …).

## Scope

Kapsule is the supported backend. Kosmos is experimental: bug reports are
welcome, but fixes may take longer to be accepted.

## License

By contributing, you agree that your contributions are licensed under the
[Apache License 2.0](LICENSE).
