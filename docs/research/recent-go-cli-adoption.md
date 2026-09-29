# Recent Go CLI adoption

Research date: **2026-09-29**. The newer-application sample covers exactly two CLI applications, not libraries.
A separate section records four established comparators supplied by the parent researcher.

## Selection and limits

Discovery used GitHub's [repository search API](https://api.github.com/search/repositories?q=language%3AGo%20created%3A%3E%3D2023-01-01%20stars%3A%3E500%20archived%3Afalse&sort=stars&order=desc&per_page=100), with `language:Go created:>=2023-01-01 stars:>500 archived:false`.
The first 100 results included witr. A [name-filtered follow-up](https://api.github.com/search/repositories?q=language%3AGo%20created%3A%3E%3D2023-01-01%20stars%3A%3E500%20sesh%20in%3Aname) found sesh.
Both are CLI applications under different personal GitHub accounts, with repository pushes within 90 days of this research date.
This is a purposive sample, not a popularity ranking or a measure of ecosystem market share.
The sample excludes gh, Crush, gogcli, sag, sonoscli, Gum, lazygit, k9s, Task, and chezmoi.

This worker cached and inspected only these two repositories. No downloaded application code was run.
Inspection covered module declarations, command entry points, help/completion wiring, configuration parsing, and representative presentation imports.
Dependency classifications below follow `go.mod`, with application imports checked separately. Indirect dependencies are not evidence of application-level adoption.
Missing libraries do not establish missing features. Runtime behavior and completion quality were not tested.

## Repository snapshots

Stars and timestamps are GitHub API observations on the research date. `pushed_at` describes repository activity, not necessarily the inspected default-branch commit.

| Application | Stars | Created (UTC) | Last push (UTC) | Latest release |
|---|---:|---|---|---|
| [joshmedeski/sesh](https://api.github.com/repos/joshmedeski/sesh), tmux session manager | 2,845 | 2023-12-27 22:47:28 | 2026-09-21 20:11:05 | [v2.31.0](https://github.com/joshmedeski/sesh/releases/tag/v2.31.0), published 2026-09-21 15:53:13 UTC |
| [pranshuparmar/witr](https://api.github.com/repos/pranshuparmar/witr), process/port ancestry inspector | 22,539 | 2025-12-20 17:26:59 | 2026-08-15 22:55:12 | [v0.3.3](https://github.com/pranshuparmar/witr/releases/tag/v0.3.3), published 2026-06-24 06:48:38 UTC |

Latest-release metadata came from each repository's API endpoint: [sesh](https://api.github.com/repos/joshmedeski/sesh/releases/latest), [witr](https://api.github.com/repos/pranshuparmar/witr/releases/latest).
Source inspection used default-branch HEAD, not release tags:

- sesh: `e1c00d5793c276d2f73146e2c83083e6aaf24ea3`, commit date `2026-09-21T15:09:17-05:00`.
- witr: `dc4fa1da82d3e266fcbd928641b4f30b3077c64f`, commit date `2026-08-08T03:33:54Z`.

## Verified adoption

### sesh: Cobra plus Fang, with a separate TUI stack

The [module file](https://github.com/joshmedeski/sesh/blob/e1c00d5793c276d2f73146e2c83083e6aaf24ea3/go.mod) directly requires Cobra **v1.10.2**, Fang **v2.0.1**, Lip Gloss **v2.0.5**, Bubble Tea **v2.0.8**, Bubbles **v2.2.1**, and go-toml/v2 **v2.4.3**.

- **Parser/help:** [`NewRootCommand`](https://github.com/joshmedeski/sesh/blob/e1c00d5793c276d2f73146e2c83083e6aaf24ea3/seshcli/root_command.go) constructs Cobra commands. [`main.go`](https://github.com/joshmedeski/sesh/blob/e1c00d5793c276d2f73146e2c83083e6aaf24ea3/main.go) passes that root to `fang.Execute`, with Fang's ANSI color scheme. Fang complements Cobra, rather than replacing its parser.
- **Presentation:** The interactive picker imports [Bubbles text input, Bubble Tea, and Lip Gloss](https://github.com/joshmedeski/sesh/blob/e1c00d5793c276d2f73146e2c83083e6aaf24ea3/picker/tui.go). These are direct application dependencies, not merely Fang's transitive dependencies.
- **Configuration:** The application [decodes TOML itself](https://github.com/joshmedeski/sesh/blob/e1c00d5793c276d2f73146e2c83083e6aaf24ea3/configurator/configurator.go) through `go-toml/v2`. The root exposes `--config`. Koanf and its providers appear as **indirect** module requirements, not the verified application configuration implementation.
- **Completion:** The [README](https://github.com/joshmedeski/sesh/blob/e1c00d5793c276d2f73146e2c83083e6aaf24ea3/README.md#shell-completion) documents `sesh completion` for Bash, Zsh, Fish, and PowerShell. The Cobra root retains default completion behavior. No separate completion library is declared. This does not establish dynamic session-name completion.

`pflag` is indirect. Neither Kong nor Carapace appears in this module file.

### witr: Cobra, custom ordinary output, and a Charm TUI

The [module file](https://github.com/pranshuparmar/witr/blob/dc4fa1da82d3e266fcbd928641b4f30b3077c64f/go.mod) directly requires Cobra **v1.10.2**, Lip Gloss **v1.1.0**, Bubble Tea **v1.3.10**, and Bubbles **v1.0.0**.

- **Parser/help:** The [binary entry](https://github.com/pranshuparmar/witr/blob/dc4fa1da82d3e266fcbd928641b4f30b3077c64f/cmd/witr/main.go) calls `app.Execute`. The [application root](https://github.com/pranshuparmar/witr/blob/dc4fa1da82d3e266fcbd928641b4f30b3077c64f/internal/app/app.go) uses Cobra command descriptions, examples, flags, and `rootCmd.Execute()`. No Fang dependency is declared.
- **Presentation:** The [interactive model](https://github.com/pranshuparmar/witr/blob/dc4fa1da82d3e266fcbd928641b4f30b3077c64f/internal/tui/model.go) imports Bubble Tea, Lip Gloss, and Bubbles table/textinput/viewport. Ordinary output also has [application-owned ANSI colors](https://github.com/pranshuparmar/witr/blob/dc4fa1da82d3e266fcbd928641b4f30b3077c64f/internal/output/colors.go). Its [color policy](https://github.com/pranshuparmar/witr/blob/dc4fa1da82d3e266fcbd928641b4f30b3077c64f/internal/app/color.go) honors `--no-color`, `NO_COLOR`, and terminal detection through direct dependency `go-isatty`.
- **Configuration:** The inspected command layer uses flags and environment reads. It declares no Viper, Koanf, or TOML dependency. YAML v3 is **indirect**. This inspection does not claim that all forms of persistent application state are absent.
- **Completion:** The root explicitly calls `InitDefaultCompletionCmd()` and enables Cobra's default completion command. The [README](https://github.com/pranshuparmar/witr/blob/dc4fa1da82d3e266fcbd928641b4f30b3077c64f/README.md) documents flag completion for Bash, Zsh, Fish, and PowerShell. No separate completion library is declared.

`pflag` is indirect. Neither Kong nor Carapace appears in this module file.

## Established comparators: parent-verified evidence

The parent researcher supplied these findings and exact source snapshots. This worker did not clone or independently inspect these four repositories.
All four were unarchived, with repository pushes on September 28 or 29, 2026.
The SHAs identify inspected main-branch snapshots. Release dates provide activity context, not evidence about dependencies inside release binaries.

| Application | Stars | Created | Inspected SHA | Latest release |
|---|---:|---|---|---|
| [lazygit](https://github.com/jesseduffield/lazygit) | 82,764 | 2018 | `a3fae72578e472b11b9763c7d5536c6853f31da4` | [v0.65.1](https://github.com/jesseduffield/lazygit/releases/tag/v0.65.1), 2026-09-13 |
| [k9s](https://github.com/derailed/k9s) | 34,705 | 2019 | `b77c8c24f0c63272809b31ddcc0946f3cd8641a8` | [v0.51.0](https://github.com/derailed/k9s/releases/tag/v0.51.0), 2026-06-06 |
| [Task](https://github.com/go-task/task) | 16,199 | 2017 | `72680fa43c8c781ac5368226d0d26df28691480a` | [v3.53.1](https://github.com/go-task/task/releases/tag/v3.53.1), 2026-08-18 |
| [chezmoi](https://github.com/twpayne/chezmoi) | 21,768 | 2018 | `24b71e4cf9d98cce0801cfc68e7553355efeaff7` | [v2.73.0](https://github.com/twpayne/chezmoi/releases/tag/v2.73.0), 2026-09-28 |

- **lazygit:** The [module](https://github.com/jesseduffield/lazygit/blob/a3fae72578e472b11b9763c7d5536c6853f31da4/go.mod) uses flaggy v1.8.0. The [entry point](https://github.com/jesseduffield/lazygit/blob/a3fae72578e472b11b9763c7d5536c6853f31da4/pkg/app/entry_point.go) calls `flaggy.Parse`. The [GUI](https://github.com/jesseduffield/lazygit/blob/a3fae72578e472b11b9763c7d5536c6853f31da4/pkg/gui/gui.go) uses local `pkg/gocui`, whose [implementation](https://github.com/jesseduffield/lazygit/blob/a3fae72578e472b11b9763c7d5536c6853f31da4/pkg/gocui/gui.go) uses tcell/v3. Vendored flaggy [enables `ShowCompletion` by default](https://github.com/jesseduffield/lazygit/blob/a3fae72578e472b11b9763c7d5536c6853f31da4/vendor/github.com/integrii/flaggy/parser.go). Its [completion implementation](https://github.com/jesseduffield/lazygit/blob/a3fae72578e472b11b9763c7d5536c6853f31da4/vendor/github.com/integrii/flaggy/completion.go) generates Bash, Zsh, Fish, PowerShell, and Nushell scripts.
- **k9s:** The [root command](https://github.com/derailed/k9s/blob/b77c8c24f0c63272809b31ddcc0946f3cd8641a8/cmd/root.go) uses Cobra and registers dynamic completion for context, cluster, user, and namespace flags. The [view application](https://github.com/derailed/k9s/blob/b77c8c24f0c63272809b31ddcc0946f3cd8641a8/internal/view/app.go) uses derailed's tview/tcell forks.
- **Task:** The [entry point](https://github.com/go-task/task/blob/72680fa43c8c781ac5368226d0d26df28691480a/cmd/task/task.go) uses pflag, not Cobra. [Interactive input](https://github.com/go-task/task/blob/72680fa43c8c781ac5368226d0d26df28691480a/internal/input/input.go) uses Bubble Tea, Bubbles, and Lip Gloss v2. [`completion.go`](https://github.com/go-task/task/blob/72680fa43c8c781ac5368226d0d26df28691480a/completion.go) embeds five shell scripts, including Fish. The [`internal/complete` engine](https://github.com/go-task/task/tree/72680fa43c8c781ac5368226d0d26df28691480a/internal/complete) handles dynamic completion.
- **chezmoi:** The [module](https://github.com/twpayne/chezmoi/blob/24b71e4cf9d98cce0801cfc68e7553355efeaff7/go.mod) uses Cobra v1.10.2. The [multiple-choice prompt](https://github.com/twpayne/chezmoi/blob/24b71e4cf9d98cce0801cfc68e7553355efeaff7/internal/chezmoibubbles/multichoiceinputmodel.go) uses Bubble Tea, Bubbles, and Lip Gloss v1. The [completion command](https://github.com/twpayne/chezmoi/blob/24b71e4cf9d98cce0801cfc68e7553355efeaff7/internal/cmd/completioncmd.go) calls Cobra generators for Bash, Fish, Zsh, and PowerShell.

The parent found no Fang direct dependency in these four modules. Configuration-library choices are outside the supplied comparator evidence.

## Decision relevance

Both newer independent examples use **Cobra**, and both use **Lip Gloss plus Bubble Tea/Bubbles** for interactive UI.
Only sesh adds **Fang** around Cobra in this six-application comparison. Both newer examples expose Cobra completion without a separate completion library.
The established comparators add two Cobra users, one flaggy user, and one pflag user. Their presentation and completion approaches differ.
This supports Cobra plus optional presentation packages as a current adoption pattern, not a finding that Kong is inferior or uncommon.
The examples also separate parser choice from configuration parsing and full-screen UI needs. They do not justify a TUI dependency for a noninteractive mail CLI.
