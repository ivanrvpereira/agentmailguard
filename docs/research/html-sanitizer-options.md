# HTML sanitizer options

Research date: **2026-09-30**.
This review compares two Go sanitizers with the current guard's visible-text extraction requirement.
It is not a security audit or a performance benchmark.

## Conclusion

Both libraries can remove browser-dangerous markup.
Neither library's tested default policy removes ordinary elements because CSS makes them invisible.
Replacing the current CSS visibility filter with either default sanitizer would change the extracted text.
The six diagnosed messages exceeded the current CSS work estimate, not an HTML sanitizer's XSS check.

Use bluemonday as the first candidate if a configurable HTML sanitizer is needed.
Do not add it solely to fix the current selector-work estimate.
A sanitizer followed by a text converter can eliminate CSS matching only if the project deliberately screens all retained text, including originally hidden text.
That design changes the visible-text contract and needs separate approval and evaluation.

## Checked sources

| Library | Default-branch commit | Commit date | Highest local version tag | License |
| --- | --- | --- | --- | --- |
| [bluemonday](https://github.com/microcosm-cc/bluemonday/tree/aba082a57c7522316c772ab0dec7e5dbc9833bfa) | `aba082a57c7522316c772ab0dec7e5dbc9833bfa` | 2025-04-04 | `v1.0.27` | [BSD-3-Clause](https://github.com/microcosm-cc/bluemonday/blob/aba082a57c7522316c772ab0dec7e5dbc9833bfa/LICENSE.md) |
| [htmlsanitizer](https://github.com/SYM01/htmlsanitizer/tree/76d8690d0f9a0cf5b4acb3ea480b095de5afbb39) | `76d8690d0f9a0cf5b4acb3ea480b095de5afbb39` | 2026-05-10 | `v1.1.1` | [MIT](https://github.com/SYM01/htmlsanitizer/blob/76d8690d0f9a0cf5b4acb3ea480b095de5afbb39/LICENSE) |

Commit dates and tags came from the cached repositories.
They do not establish release dates, support guarantees, or comparative security.

## bluemonday

The library provides configurable element, attribute, URL, and inline-style policies.
Its default `UGCPolicy` does not permit `style` attributes or user-supplied classes; see [policies.go](https://github.com/microcosm-cc/bluemonday/blob/aba082a57c7522316c772ab0dec7e5dbc9833bfa/policies.go#L59-L65) and [style exclusions](https://github.com/microcosm-cc/bluemonday/blob/aba082a57c7522316c772ab0dec7e5dbc9833bfa/policies.go#L179-L187).
It uses the `golang.org/x/net/html` tokenizer; see the [technical summary](https://github.com/microcosm-cc/bluemonday/blob/aba082a57c7522316c772ab0dec7e5dbc9833bfa/README.md#technical-summary).

Inline-style sanitization validates declarations against allowed properties and values.
It does not compute stylesheet matching, inheritance, or whether an element's text is visible; see [sanitizeStyles](https://github.com/microcosm-cc/bluemonday/blob/aba082a57c7522316c772ab0dec7e5dbc9833bfa/sanitize.go#L813-L910).
The README's limitations section contains an older CSS statement; the implemented inline-style support is the stronger evidence.
Its [HTML-email example](https://github.com/microcosm-cc/bluemonday/blob/aba082a57c7522316c772ab0dec7e5dbc9833bfa/cmd/sanitise_html_email/main.go) explicitly warns that preserving styling comes at the expense of security.
That example is not a visible-text extractor.

The [module file](https://github.com/microcosm-cc/bluemonday/blob/aba082a57c7522316c772ab0dec7e5dbc9833bfa/go.mod) declares douceur and x/net, with gorilla/css indirect.
These dependency families already exist in this project, but bluemonday itself is not a project dependency.

## htmlsanitizer

This library exposes an allowlist, an optional URL handler, and a streaming writer; see [sanitizer.go](https://github.com/SYM01/htmlsanitizer/blob/76d8690d0f9a0cf5b4acb3ea480b095de5afbb39/sanitizer.go).
Its [module file](https://github.com/SYM01/htmlsanitizer/blob/76d8690d0f9a0cf5b4acb3ea480b095de5afbb39/go.mod) declares no external dependencies.
The README claims O(n) processing through a finite-state machine; [dfa.go](https://github.com/SYM01/htmlsanitizer/blob/76d8690d0f9a0cf5b4acb3ea480b095de5afbb39/dfa.go) implements that parser.
This review did not independently prove the complexity bound or browser-parser equivalence.

The [default allowlist](https://github.com/SYM01/htmlsanitizer/blob/76d8690d0f9a0cf5b4acb3ea480b095de5afbb39/tags.go#L176-L285) allows classes and IDs, but not inline styles or `hidden` attributes.
It treats script, style, and object content specially.
Its attribute handler validates allowed names and URL values, not CSS visibility; see [safeAppendAttr](https://github.com/SYM01/htmlsanitizer/blob/76d8690d0f9a0cf5b4acb3ea480b095de5afbb39/dfa.go#L113-L140).

## Executed synthetic probes

These probes used invented HTML only, not mailbox data.
Each public sanitizer processed the following inputs:

| Input | bluemonday `UGCPolicy` output | htmlsanitizer default output |
| --- | --- | --- |
| `<style>.h{display:none}</style><p class="h">HIDDEN</p><p>VISIBLE</p>` | `<p>HIDDEN</p><p>VISIBLE</p>` | `<p class="h">HIDDEN</p><p>VISIBLE</p>` |
| `<p style="display:none">HIDDEN</p><p>VISIBLE</p>` | `<p>HIDDEN</p><p>VISIBLE</p>` | `<p>HIDDEN</p><p>VISIBLE</p>` |
| `<p hidden>HIDDEN</p><p>VISIBLE</p>` | `<p>HIDDEN</p><p>VISIBLE</p>` | `<p>HIDDEN</p><p>VISIBLE</p>` |
| `<script>alert(1)</script><a href="javascript:alert(1)">CLICK</a>` | `CLICK` | `<a>CLICK</a>` |

Both retain the text labelled `HIDDEN` while removing its hiding mechanism.
Both remove the tested script and JavaScript URL.
These four cases are evidence of the tested behavior, not proof against every XSS or prompt-injection attack.

Both upstream `go test -race ./...` runs passed.
The htmlsanitizer run used its unchanged module.
The bluemonday run used a temporary workspace with the project's cached x/net v0.58.0 instead of upstream's v0.26.0.
Dependency downloads were disabled, and project `go.mod` and `go.sum` stayed unchanged.
No library was installed or adopted, and no application code changed during this comparison.
