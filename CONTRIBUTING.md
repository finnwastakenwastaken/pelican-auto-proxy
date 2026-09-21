# Contributing

## If you are an AI agent

Read [`CLAUDE.md`](CLAUDE.md) first, in full, before changing anything. It has hard rules — no private
infrastructure values or secrets, final naming, gates that must pass, docs updated in the same commit as the
behaviour they describe — that apply regardless of which tool or model you are. It also points at
[`docs/dev/decisions.md`](docs/dev/decisions.md) for why things are the way they are, so you don't need to
rediscover it by trial and error.

## Gates

All of these run in CI (`.github/workflows/ci.yml`) and are runnable locally through Docker, without installing Go,
PHP or Node on your own machine:

| Gate | Command (see `ci.yml` for the exact container/flags) |
|---|---|
| Go vet + tests | `go vet ./... && go test ./...` in `agent/` |
| Golden nftables files | `nft -c -f` against `agent/internal/nft/testdata/*.nft` |
| Shell | `shellcheck` and `bash -n` on every script |
| PHP | `php -l` on every plugin file, plus `php test/rules-test.php` |
| Infra sweep | `scripts/infra-sweep.sh` |
| Workflow lint | `actionlint` against `.github/workflows/*.yml` |

A pull request that doesn't pass these won't be merged. If you add a new gate, break it once on purpose first and
confirm it actually fails — a check that has never been seen failing might not be checking anything.

## Style

- **Plain English in every doc, error message and commit that a stranger might read.** This project's audience is
  someone running Pelican Panel at home, not someone who already knows this codebase. Consequence first, one idea
  per sentence, every command copy-paste ready.
- **No private infrastructure values, ever** — not in code, tests, docs, or commit messages. Use the documentation
  IP ranges (`192.0.2.0/24`, `198.51.100.0/24`, `203.0.113.0/24`) or plain RFC1918 examples (`10.0.0.10`) in any
  example. `scripts/infra-sweep.sh` enforces this; it must pass on the tree and on any zip you build.
- **No secrets in git.** Tokens, keys and certificates are `.gitignore`d; never force-add them.
- **Names are final** (see `CLAUDE.md` §"Names are final") — don't introduce a new name for something that already
  has one.
- **Every behaviour change updates the matching docs page and `CHANGELOG.md` entry in the same commit.** A flag, a
  page, a command, or a message that changes without its doc changing is treated as an incomplete change, not a
  follow-up someone else can do later.
- **Commit messages explain why, not just what** — the diff already shows what changed.

## Where things live

See [docs/dev/architecture.md](docs/dev/architecture.md) for the component layout and design, and
[docs/dev/testing.md](docs/dev/testing.md) for how to exercise a change without any live VPS or panel.
