# Peter Steinberger's Go CLI choices

Research date: 2026-09-29 UTC.

## gogcli

Inspected `openclaw/gogcli` at commit `3a4bbf23d815557d0d481e73f740300f016375b2`.
The findings describe this source snapshot, not every project by Peter Steinberger.

| Concern | Implementation | Evidence |
| --- | --- | --- |
| Commands and flags | Kong v1.16.1 | [go.mod](https://github.com/openclaw/gogcli/blob/3a4bbf23d815557d0d481e73f740300f016375b2/go.mod), [root.go](https://github.com/openclaw/gogcli/blob/3a4bbf23d815557d0d481e73f740300f016375b2/internal/cmd/root.go) |
| Help presentation | Custom help printer calls `kong.DefaultHelpPrinter`, then adjusts sections and adds colors through `termenv` | [help_printer.go](https://github.com/openclaw/gogcli/blob/3a4bbf23d815557d0d481e73f740300f016375b2/internal/cmd/help_printer.go) |
| Output colors | `muesli/termenv` with `auto`, `always`, and `never` modes | [ui.go](https://github.com/openclaw/gogcli/blob/3a4bbf23d815557d0d481e73f740300f016375b2/internal/ui/ui.go) |
| Configuration | Custom configuration structs, JSON output, and JSON5 input | [config.go](https://github.com/openclaw/gogcli/blob/3a4bbf23d815557d0d481e73f740300f016375b2/internal/config/config.go) |

The module does not list Cobra, Fang, Bubble Tea, Lip Gloss, Viper, or Koanf.
The help printer respects `NO_COLOR` and suppresses help colors for `--plain` and `--json` unless earlier option precedence selects another mode.
The implementation keeps generated help and custom presentation separate.

## Two other repositories

The bounded profile check inspected two other Go CLI repositories, not every repository on the account.

- [`steipete/sag`](https://github.com/steipete/sag/blob/410fdccd2c3a2e6d645d5eba51c23896f975365e/go.mod) uses Cobra v1.10.2 and pflag v1.0.10. Its module lists no Fang, Lip Gloss, Kong, Koanf, or Viper.
- [`steipete/sonoscli`](https://github.com/steipete/sonoscli/blob/b7850a276c8c549532f3fcd88c29dad68facf2b4/go.mod) uses Cobra v1.10.2 with pflag and mousetrap as its only indirect module requirements. Its [configuration store](https://github.com/steipete/sonoscli/blob/b7850a276c8c549532f3fcd88c29dad68facf2b4/internal/appconfig/store.go) uses standard-library JSON and file operations.

These examples establish that his repositories do not share one command framework.
They do not establish a universal preference for Kong or Cobra.
The parent independently inspected both module files and the sonoscli configuration store.

## Implications for agentmailguard-go

Kong plus a small custom presentation layer is a viable alternative to Cobra plus Fang.
It requires project-owned help formatting rather than Fang's prepared styling.
Existing TOML configuration can remain unchanged with either choice.
Neither choice requires a full TUI framework.
This research does not authorize a dependency or application change.
