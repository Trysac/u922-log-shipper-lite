# Contributing

Thanks for helping improve `github.com/Trysac/u922-log-shipper-lite`.

## Workflow

1. Branch from `main`; never push to `main` directly.
2. `go mod download` to fetch modules (none are needed today).
3. Write a failing test, then the change.
4. `go run ./scripts/check` must pass.
5. Update `CHANGELOG.md` under **Unreleased** and the docs the change affects.
6. Open a pull request describing the problem and the solution.

## Commit messages

Use [Conventional Commits](https://www.conventionalcommits.org/):

```text
feat: add strict mode to Normalize
fix: reject tab-only input
docs: document error values
```

`feat` bumps the minor version, `fix` the patch version, and a `BREAKING CHANGE:` footer the major
version. Releases are git tags `vMAJOR.MINOR.PATCH`; a major version >= 2 changes the module path.

## Code of conduct

Be respectful and constructive. Maintainers may close contributions that ignore this guide.
