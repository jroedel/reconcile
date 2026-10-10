# reconcile

Reconcile helps the people who handle a nonprofit's money day to day
understand it between the accountant's reports, and talk with the
accountant about it. It does not replace the bookkeeping software or the
accountant. It reads what the bank says, and lets the people closest to the
money say what each movement was:

- **income**: money that came to the organization;
- **an expense**: money the organization spent (a refund is a negative
  expense, not income);
- **a transfer**: money moving between the organization's own accounts, such
  as paying the credit card from checking;
- **pass-through**: money that went through the organization's accounts but
  was never its own, such as a personal charge on its card and the repayment
  that answers it, or a collection forwarded to the diocese.

Only income and expenses are the organization's operations. Transfers and
pass-through are kept and shown, and each should come back to zero, but
they are never counted as earning or spending.

So: upload bank and card statements, check them against their own
balances, say what each part of each charge was, attach receipts, follow
projects that draw from several accounts, reconcile each month, and hand the
accountant a package.

[`docs/plan.md`](docs/plan.md) is the plan and the build order;
[`docs/design.md`](docs/design.md) is how it should feel to use.

## Using it from Claude

Claude on claude.ai can connect to the site and read your books as you,
seeing only what you can: [`docs/claude.md`](docs/claude.md) says how to
connect it and what to ask. A Google Apps Script in your own account can
send the statements your email brings to your inbox on the site:
[`apps-script/gmail-inbox`](apps-script/gmail-inbox/README.md).

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
