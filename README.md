# reconcile

A web app for the people who handle a nonprofit's money day to day. Upload
bank and card statements, check them against their own balances, attach
receipts, follow projects that draw from several accounts, reconcile each
month, and hand the accountant a package.

[`docs/plan.md`](docs/plan.md) is the plan and the build order;
[`docs/design.md`](docs/design.md) is how it should feel to use.

## Running it

Go 1.26, no other toolchain.

Put `secrets.env` from Bitwarden in the clone, and:

```sh
make run            # serves on 127.0.0.1:8461; makes config.toml from secrets.env the first time
make test           # unit tests, lint, shell tests
make hooks          # the pre-commit guard that keeps statements out of git
make help           # every target
```

Sign-in codes are written to the log on a developer's machine: the local
config has no mail relay, and never production's. Without a `secrets.env`,
`cp config.example.toml config.toml` first.

## Deploying

A merge to `main` deploys, through `.github/workflows/deploy.yml`. Setting up
the server and the credentials is in [`deploy/README.md`](deploy/README.md);
the hostname and the account are kept out of this public repository, in a
`secrets.env` that never leaves a person's machine.

## Contributing

Changes arrive as pull requests, and a person reviews and merges each one.
`CLAUDE.md` (also `AGENTS.md`) holds the rules for writing code here.

## License

Copyright 2026 Jeff Roedel.

Licensed under the Apache License, Version 2.0; see [`LICENSE`](LICENSE).
