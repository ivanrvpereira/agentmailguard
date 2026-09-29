# Go CLI and TUI ecosystem

Research date: 2026-09-29 UTC.

## Conclusion

The proposed list covers established choices, but it mixes different layers.
Cobra, Kong, and urfave/cli handle commands and arguments.
Fang adds presentation to Cobra, rather than replacing it.
Lip Gloss provides terminal styling, not a complete interactive application framework.
Bubble Tea supplies the interactive application model, with Bubbles components and Huh forms.
A recent library is not automatically a better choice than an established library with recent releases.

For this project, Cobra plus optional Fang fits a command/help redesign.
Bubble Tea plus Lip Gloss fits a separate interactive mailbox interface, not merely nicer help.
Fang remains explicitly experimental in its [official README](https://github.com/charmbracelet/fang#readme), despite its v2 release.

## GitHub snapshot

The parent researcher collected these values through `gh api` on the research date.
Repository links in the Stars column identify the API source.
Release links identify `/releases/latest`, except Kong, which uses tags.
All listed repositories were unarchived.

| Project | Stars | Latest release or tag | Date |
| --- | ---: | --- | --- |
| Cobra | [44,672](https://api.github.com/repos/spf13/cobra) | [v1.10.2](https://api.github.com/repos/spf13/cobra/releases/latest) | 2025-12-04 |
| urfave/cli | [24,270](https://api.github.com/repos/urfave/cli) | [v3.13.0](https://api.github.com/repos/urfave/cli/releases/latest) | 2026-09-20 |
| Kong | [3,184](https://api.github.com/repos/alecthomas/kong) | [v1.16.1 tag](https://api.github.com/repos/alecthomas/kong/tags) | 2026-08-09, tag commit |
| Fang | [1,953](https://api.github.com/repos/charmbracelet/fang) | [v2.0.1](https://api.github.com/repos/charmbracelet/fang/releases/latest) | 2026-03-11 |
| Bubble Tea | [45,178](https://api.github.com/repos/charmbracelet/bubbletea) | [v2.0.10](https://api.github.com/repos/charmbracelet/bubbletea/releases/latest) | 2026-09-24 |
| Lip Gloss | [11,877](https://api.github.com/repos/charmbracelet/lipgloss) | [v2.0.6](https://api.github.com/repos/charmbracelet/lipgloss/releases/latest) | 2026-08-11 |
| Bubbles | [8,953](https://api.github.com/repos/charmbracelet/bubbles) | [v2.2.1](https://api.github.com/repos/charmbracelet/bubbles/releases/latest) | 2026-08-24 |
| Huh | [7,183](https://api.github.com/repos/charmbracelet/huh) | [v2.0.3](https://api.github.com/repos/charmbracelet/huh/releases/latest) | 2026-03-10 |
| tview | [14,115](https://api.github.com/repos/rivo/tview) | [v0.42.0](https://api.github.com/repos/rivo/tview/releases/latest) | 2025-08-27 |
| tcell | [5,223](https://api.github.com/repos/gdamore/tcell) | [v3.5.0](https://api.github.com/repos/gdamore/tcell/releases/latest) | 2026-09-11 |
| reeflective/console | [117](https://api.github.com/repos/reeflective/console) | [v0.5.0](https://api.github.com/repos/reeflective/console/releases/latest) | 2026-07-18 |

These are not direct substitutes: tview supplies widgets, tcell supplies terminal infrastructure, and reeflective/console supplies an interactive console.
Release age alone does not establish inactivity: tview had an August 2026 push despite its older release.

## Two newer contenders

| Project | Verified status | Relevance and limits |
| --- | --- | --- |
| [Ultraviolet](https://github.com/charmbracelet/ultraviolet/blob/666ce5eec9fc406a5f244c2f5f634ed5cd2c3876/README.md) | [Created 2025-05-06, 390 stars, pushed 2026-09-29](https://api.github.com/repos/charmbracelet/ultraviolet). | Low-level terminal input and cell rendering. Its README identifies it as the foundation for Bubble Tea v2 and Lip Gloss v2. Standalone use is possible, but its API can change. This complements the shortlist rather than displacing Bubble Tea. |
| [Glyph](https://github.com/kungfusheep/glyph/blob/a67d7a519cdffc5fe3aaa52bd4664fa14ba96383/README.md) | [Created 2026-01-25, 151 stars](https://api.github.com/repos/kungfusheep/glyph). [v0.8.0 released 2026-08-28](https://github.com/kungfusheep/glyph/releases/tag/v0.8.0). | Declarative TUI with layouts, tables, inputs, and tree views. Its README states pre-1.0 API evaluation and macOS/Linux support only. It is a credible newer alternative, not evidence of comparable adoption. |

Repository creation dates do not necessarily identify the first public release.
Neither contender establishes that a newer framework is more popular than the established shortlist.

## Verified application adoption

1. **GitHub CLI:** its [direct dependencies](https://github.com/cli/cli/blob/0f6de7b4f0aef53a94505efc2d57e87c578196f7/go.mod) include Cobra, Bubble Tea v2, Bubbles v2, Lip Gloss v2, and Huh v2. Its [searchable multiselect implementation](https://github.com/cli/cli/blob/0f6de7b4f0aef53a94505efc2d57e87c578196f7/internal/prompter/multi_select_with_search.go) imports all four Charm packages. This is concrete interactive-prompt use inside an ordinary CLI, not proof that all of `gh` is a fullscreen TUI.
2. **Charm Crush:** its [direct dependencies](https://github.com/charmbracelet/crush/blob/89c3c4a7e19d9d2e2e5818b5a9a524cc7ae66f2d/go.mod) include Cobra, Fang v2, Bubble Tea v2, Bubbles v2, Lip Gloss v2, and Ultraviolet. Its [root command](https://github.com/charmbracelet/crush/blob/89c3c4a7e19d9d2e2e5818b5a9a524cc7ae66f2d/internal/cmd/root.go#L211-L219) calls `fang.Execute` with a Cobra command. This verifies coexistence of command presentation and a TUI stack. Crush is a first-party Charm application, not independent adoption evidence.

Current source uses `charm.land/.../v2` module paths for several Charm packages.
Examples that use older `github.com/charmbracelet/...` imports can target a different API generation.

## Application to agentmailguard-go

The [approved plan](../../PLAN.md) specifies a thin HTTP CLI, standard-library preference, and restricted dependency categories.
Any CLI/TUI dependency needs explicit approval before implementation.
A visual redesign must preserve JSON output, exit codes, remote-client separation, and fail-closed body screening.
A TUI must consume guarded server views rather than direct provider data.
No library choice by itself establishes safe handling of hostile email text or terminal control sequences.

## Method and limitations

Discovery used GitHub repository search for recent Go CLI/parser and TUI projects, plus repository and release API metadata.
Source inspection used reusable librarian checkouts and commit-pinned citations.
The search was bounded, not an exhaustive census.
Stars measure attention, not installations, reliability, maintainability, or suitability.
Dependency evidence establishes source adoption, not deployment volume or the dependency versions in every published binary.
No contender was benchmarked or executed.
No application code, dependency, configuration, plan, progress file, or credential was changed.
