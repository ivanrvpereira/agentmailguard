# Prompt injection through email: what concealment real incidents used

Research date: **2026-10-01**.
This review surveys public disclosures, vendor write-ups, telemetry reports, and competitions from 2023 to 2026 in which prompt injection arrived through email or email-like content.
It records the concealment technique, the vendor fix, and whether concealment was needed for the attack to work.
It is not an exhaustive literature review, and it did not reproduce any attack.

## Conclusion

No surveyed attack needed concealment to make the model obey; concealment only kept the payload away from a human reader.
EchoLeak, the Copilot Studio email agent, conditional injection, the Gemini calendar and email invites, and the competition winners used visible text; EchoLeak passed Microsoft's classifier through phrasing, not hiding.
Every email proof of concept that published its markup hid the payload with inline styles on one element: `font-size:0`, white or transparent `color`, `opacity:0`, 1px boxes, `mso-hide:all`, or `display:none`.
In each published payload, an inline size, opacity, or display check alone was enough; no payload depended on colour contrast alone, on `<style>` selectors, or on classes.
Class-based hiding (`visually-hidden`), contrast against inherited backgrounds, occlusion, and viewport tricks appear only in web telemetry; occlusion needs layout, which a CSS cascade does not compute.
The other recurring carriers are cheap to remove: HTML comments, non-rendered elements, Unicode tag and zero-width characters, and a `text/plain` part that differs from the HTML part.
Vendor fixes were mostly classifiers, adversarial training, and egress controls such as link and image redaction, CSP, and URL allowlists.
The only stripping fix that several vendors confirmed or that tests observed is removal of Unicode tag characters; Google declined to make it for Gemini.
No vendor described a CSS-visibility stripper as its fix; Microsoft Defender uses hidden text as one classifier signal.
This evidence supports the thesis: Jev and the agent model carry the defense, and HTML hiding removal is hygiene.
The 80/20 is an inline-only, single-pass filter plus the existing Unicode stripping; `<style>` selector matching protects against nothing in the email record.
Caveat: one web study found models followed hidden injections more often from flattened plain text (3.9%) than from HTML (1.1%), because flattening removes the "this was hidden" cue ([arXiv 2604.27202](https://arxiv.org/html/2604.27202v1)).

## Email and email-like incidents

"Necessary?" means: did the model follow the payload because it was hidden, or would visible text have worked?

| Incident | Delivery | Concealment | Necessary? | Vendor fix | Fix type |
| --- | --- | --- | --- | --- | --- |
| [EchoLeak, CVE-2025-32711](https://www.aim.security/lp/aim-labs-echoleak-blogpost) (Aim Labs, M365 Copilot, 2025) | Email retrieved by Copilot RAG | None needed; text addressed to the human recipient so the XPIA classifier missed it ([Willison quoting Aim](https://simonwillison.net/2025/Jun/11/echoleak/), [arXiv 2509.10540](https://arxiv.org/html/2509.10540v1)) | No | Server-side fix, May 2025; the chain was reference-style Markdown link/image redaction, CSP, and a Teams proxy ([arXiv 2509.10540](https://arxiv.org/html/2509.10540v1)) | Output/egress |
| [Phishing for Gemini](https://0din.ai/blog/phishing-for-gemini) (0DIN, Gmail summary, July 2025) | HTML email, "Summarize this email" | Inline `<span style="font-size:0px;color:#ffffff">` with a fake `<Admin>` tag | No; only hides it from the reader | Google cited red-team training of models and its layered-defense post ([BleepingComputer](https://www.bleepingcomputer.com/news/security/google-gemini-flaw-hijacks-email-summaries-for-phishing/)) | Classifier/training |
| [ShadowLeak](https://www.radware.com/blog/threat-intelligence/shadowleak/) (Radware, ChatGPT Deep Research + Gmail, 2025) | Email in inbox, agent browsing tool | "Tiny fonts, white-on-white text, layout tricks"; markup not published | No; success came from authority framing and Base64 encoding of the PII | Fixed in August 2025; OpenAI did not say how ([Dark Reading](https://www.darkreading.com/vulnerabilities-threats/shadowleak-chatgpt-invisibly-steal-emails)) | Unknown |
| [M365 Copilot ASCII smuggling](https://embracethered.com/blog/posts/2024/m365-copilot-prompt-injection-tool-invocation-and-data-exfil-using-ascii-smuggling/) (Rehberger, 2024) | Email or shared document | Payload visible; Unicode tags hid the exfiltrated data in a link; the author notes white font or tags could also hide the payload | No | M365 Copilot began stripping hidden characters on input but still wrote them ([Ars Technica](https://arstechnica.com/security/2024/10/ai-chatbots-can-read-and-write-invisible-text-creating-an-ideal-covert-channel/)) | Stripping (Unicode) + link handling |
| [Conditional prompt injection](https://embracethered.com/blog/posts/2024/whoami-conditional-prompt-injection-instructions/) (Rehberger, Copilot, 2024) | Email | None; "this POC doesn't attempt to hide/obfuscate the instructions" | No | Microsoft: not requiring immediate servicing | None |
| [Copilot Studio email agent](https://labs.zenity.io/post/a-copilot-studio-story-2-when-aijacking-leads-to-full-data-exfiltration-bc4a) (Zenity, 2025) | Email to an agent-monitored inbox | None; plain "Oops, there has been a mistake…" text | No | Fix in April 2025; Zenity's testing indicates a prompt-injection classifier that blocks the payload | Classifier |
| [Invitation Is All You Need](https://www.safebreach.com/blog/invitation-is-all-you-need-hacking-gemini/) (SafeBreach, Gemini, 2025) | Calendar invite title or email subject | None in content; the UI's "Show more" fold hid it | No | User confirmations, URL sanitization, content classifiers | Classifier + confirmation + URL |
| [FireTail ASCII smuggling](https://www.firetail.ai/blog/ghosts-in-the-machine-ascii-smuggling-across-various-llms) (Gemini, Sept 2025) | Calendar invite, email | Unicode tag characters | No | Google: "no action"; ChatGPT, Copilot, Claude appeared to scrub input ([BleepingComputer](https://www.bleepingcomputer.com/news/security/google-wont-fix-new-ascii-smuggling-attack-in-gemini/)) | None (Google); stripping (others) |
| [Immersive Labs SEG bypass](https://www.immersivelabs.com/resources/c7-blog/weaponizing-llms-bypassing-email-security-products-via-indirect-prompt-injection) (2025) | HTML email to Gemini | Inline `font-size:0px; line-height:0; color:#FFFFFF; mso-hide:all`; URL split into string fragments | No | None reported | n/a |
| [Shortwave AI email client](https://insinuator.net/2025/09/stealing-emails-via-prompt-injections/) (ERNW, 2025) | HTML email, including bulk "find urgent emails" runs | Inline `opacity:0; font-size:1px; color:transparent; max-width:1px` | No; hidden only because "payloads … appear increasingly suspicious to the human eye" | Memory-change display, link confirmation, system-prompt hardening, browsing paywalled | Prompt + UX + egress |
| [Permiso Copilot summaries](https://permiso.io/blog/copilot-prompt-injection-ai-email-phishing) (2026) | Email, Outlook/Teams Copilot | "Hidden or low-visibility" appended text; markup not published | No | Outlook summarize sometimes "detected suspicious content and refused"; Microsoft reproduced it in January 2026 | Classifier (observed) |
| [Forcepoint summarizer PoC](https://www.forcepoint.com/blog/x-labs/html-payload-hijacks-email-summarizer) (2026) | HTML email to an unguarded Haiku 4.5 summarizer | Inline `font-size:0px; color:#ffffff; line-height:0` | Not tested; "it does not show either working separately" | Lab PoC; advice is visible-text extraction plus hidden-style detection | n/a |
| [OpenAI Atlas red team](https://openai.com/index/hardening-atlas-against-prompt-injection/) (Dec 2025) | Seeded inbox email ("resign to CEO") | Not described | n/a | Adversarially trained agent checkpoint, monitors, system safeguards | Training/classifier |
| [Claude for Chrome](https://claude.com/blog/claude-for-chrome?lid=plObAK8Efjj2i4ev) (Anthropic, Aug 2025) | Email asking to delete mail for "mailbox hygiene" | None described | No | System prompts, permissions, confirmations, classifiers; later RL training and probes on tool results ([Anthropic](https://www.anthropic.com/news/prompt-injection-defenses), [GA post](https://claude.com/blog/claude-in-chrome-generally-available)) | Training/classifier |
| [In-the-wild AI-aware phish](https://malwr-analysis.com/2025/08/24/phishing-emails-are-now-aimed-at-users-and-ai-defenses/) (Aug 2025) | Real phishing email aimed at AI SOC triage | Prompt in the `text/plain` MIME part, which HTML clients do not show | n/a (goal was distraction, not obedience) | None | n/a |

Email-like content uses the same carriers:

| Incident | Delivery | Concealment | Vendor fix |
| --- | --- | --- | --- |
| [AgentFlayer ChatGPT Connectors](https://labs.zenity.io/p/agentflayer-chatgpt-connectors-0click-attack-5b41) (Zenity, Aug 2025) | Shared document | "1px white font"; [Wired](https://www.wired.com/story/poisoned-document-could-leak-secret-data-chatgpt/) says size-one white text | Exfil through an allowed Azure Blob image host; OpenAI's `url_safe` image check was the bypassed control |
| [CamoLeak](https://www.legitsecurity.com/blog/camoleak-critical-github-copilot-vulnerability-leaks-private-source-code) (Legit, GitHub Copilot Chat, 2025) | Pull-request description | HTML comment (GitHub's official hidden-content feature) | Disabled image rendering in Copilot Chat |
| [ForcedLeak](https://noma.security/blog/forcedleak-agent-risks-exposed-in-salesforce-agentforce) (Noma, Agentforce, 2025) | Web-to-Lead description field | None; plain text in a stored field | Trusted URL allowlist enforcement; expired CSP domain re-secured |
| [Gemini Trifecta](https://www.tenable.com/blog/the-trifecta-how-three-new-gemini-vulnerabilities-in-cloud-assist-search-model-and-browsing) (Tenable, 2025) | Log field (User-Agent), search history | UI truncation only | Stopped rendering hyperlinks; blocked browsing exfiltration; model rollback |
| [Comet](https://brave.com/blog/comet-prompt-injection/) (Brave, 2025) | Reddit comment | Spoiler tag; Brave also lists white text and HTML comments | Not fully fixed at disclosure |
| [Unseeable injections](https://brave.com/blog/unseeable-prompt-injections/) (Brave, Oct 2025) | Screenshot of a page | Faint light-blue text on yellow, read through image | n/a; text-level stripping cannot see it |
| [Google Jules](https://embracethered.com/blog/posts/2025/google-jules-invisible-prompt-injection/) (Rehberger, Aug 2025) | GitHub issue | Unicode tag characters | Not mitigated at the Gemini model or API level at publication |

## Telemetry and competitions

Web telemetry shows which tricks attackers actually ship, but web pages are not email.
Email clients and the project's provider APIs may not keep every web technique.

- Unit 42 found visible plaintext was the most common delivery (37.8%), then HTML attribute cloaking (19.8%) and CSS rendering suppression (16.9%) ([Unit 42](https://unit42.paloaltonetworks.com/ai-agent-prompt-injection/)).
- Unit 42's ad-review evasion page used 24 layered injections: inline `font-size:0; line-height:0; height:0; overflow:hidden`, off-screen offsets, `display:none`, opacity, same-colour text, Base64 in JavaScript, zero-width characters, homoglyphs, and bidi overrides ([Unit 42 case](https://github.com/PaloAltoNetworks/Unit42-timely-threat-intel/blob/main/2025-12-15-real-world-case-of-malicious-indirect-prompt-injection.md)).
- An April 2026 crawl of 1.2B URLs found about 70% of injections in non-rendered channels such as headers, comments, and metadata ([arXiv 2604.27202](https://arxiv.org/html/2604.27202v1)).
- In the same crawl, rendered-but-hidden cases were mostly colour contrast (2,397), occlusion (1,860), viewport placement (1,802), small text (1,358), and tiny elements (851); `display`/`visibility` (281), clip (108), and opacity (20) were rare.
- The same study measured compliance of 3.9% on plain text, 1.1% on HTML, and 0.2% on raw responses.
- Forcepoint's web indicators used HTML comments, `display:none`, 1px fonts, `rgba(…,0.01)`, `aria-hidden`, and the framework class `visually-hidden` ([Forcepoint](https://www.forcepoint.com/blog/x-labs/indirect-prompt-injection-payloads)).
- Google's Common Crawl scan found mostly low-sophistication injections, with a 32% relative rise in malicious ones from November 2025 to February 2026 ([Google](https://blog.google/security/prompt-injections-web/)).
- Microsoft saw multi-million-per-day phishing that used U+E0020 tag characters to split keywords and evade filters, not to instruct AI; over 99% was caught by layers that did not depend on the tag characters ([Microsoft](https://www.microsoft.com/en-us/security/blog/2026/09/03/ascii-smuggling-crosses-over-from-ai-prompt-injection-to-phishing-evasion/)).

Competitions inject plain text into tool results, so CSS hiding plays no part, and they still break every model:

- The UK AISI and Gray Swan challenge recorded indirect injection success of 27.1% versus 5.7% for direct; common winners were fake system-prompt tags such as `<|start_header_id|>system<|end_header_id|>`, faux reasoning, and fake session resets ([arXiv 2507.20526](https://arxiv.org/html/2507.20526)).
- The later IPI Arena required the attack to also stay hidden in the agent's final answer; success ranged from 0.5% (Claude Opus 4.5) to 8.5% (Gemini 2.5 Pro), and one fake-control-panel template worked in 21 of 41 scenarios ([arXiv 2603.15714](https://arxiv.org/html/2603.15714v1)).
- The HackAPrompt 2.0 agent-track winners reported that the intent is "contextual, not lexical" and relied on policy and authority framing ([RED_CORE write-up](https://codeberg.org/red-core/H.A.P.-2.0-MATSxTrails-RED_CORE-Writeup)).
- Lakera's B³ benchmark found that stronger system prompts and a self-judge reduce but never remove risk ([B³](https://b3.lakera.ai/)).

## Technique recurrence and removal cost

"Cheap" means one pass over tokens or one element's own attributes, with no selector matching and no layout.

| Technique | Where it appeared | Cheap removal? | Needs cascade or layout? |
| --- | --- | --- | --- |
| Inline `font-size:0` or ≤1px | 0DIN, Immersive, Shortwave, Forcepoint email; Unit 42, Forcepoint web | Yes | No |
| Inline `display:none`, `visibility:hidden`, `hidden` attribute | Microsoft Defender example; Unit 42, Forcepoint web | Yes | No |
| Inline `opacity:0` or near-zero alpha | Shortwave; Unit 42, Forcepoint web | Yes | No |
| Inline zero or 1px `width`/`height`/`max-width`, or `height:0;overflow:hidden` | Shortwave; Unit 42 | Yes | No |
| `mso-hide:all` | Immersive | Yes | No |
| Inline white or transparent `color` | 0DIN, Immersive, Shortwave, Forcepoint; never the only hiding property | Partly: drop near-white or transparent text when no inline background is set on it or its ancestors | Full contrast needs inherited backgrounds |
| Inline off-screen `position`/`left`/`text-indent` | Unit 42 web | Yes, with a threshold | No |
| HTML comments | CamoLeak; Comet; Forcepoint web; arXiv crawl | Yes (`html.go` already emits only text nodes) | No |
| `<script>`, `<style>`, `<head>`, `<template>`, metadata | Unit 42 (dynamic JS); arXiv crawl | Yes (already in `dropTags`) | No |
| Unicode tag block U+E0000–E007F, zero-width, bidi | Copilot, FireTail, Jules; Unit 42; Microsoft phish | Yes (`stripInvisible` already drops `Cc`, `Cf`, and U+E0100–E01EF) | No |
| Base64 or entity encoding | Unit 42; ShadowLeak used Base64 only for output | Entities decode in parsing; Base64 blobs can be flagged, not removed safely | No |
| Divergent `text/plain` alternative | In-the-wild AI-aware phish | Depends on which part the provider returns | No |
| Class or `<style>` selector hiding (`.h{display:none}`, `visually-hidden`) | Forcepoint web only | No | Selector matching |
| Contrast against stylesheet backgrounds | arXiv crawl (web) | No | Cascade |
| Occlusion, z-index, viewport position | arXiv crawl (web) | No | Layout engine; a cascade alone is not enough |
| Text in images | Brave screenshots | No | OCR; outside text filtering |
| Fake chat-template or `<Admin>` tags | 0DIN; Gray Swan winners | Detectable, but the words remain | No |

## Vendor fix patterns

Classifier, training, or prompt-based fixes: Google for Gemini in Gmail and for SafeBreach ([Google](https://security.googleblog.com/2025/06/mitigating-prompt-injection-attacks.html)), Microsoft for Copilot Studio ([Zenity](https://labs.zenity.io/post/a-copilot-studio-story-2-when-aijacking-leads-to-full-data-exfiltration-bc4a)), OpenAI for Atlas ([OpenAI](https://openai.com/index/hardening-atlas-against-prompt-injection/), [OpenAI](https://openai.com/index/prompt-injections/)), Anthropic for Claude in Chrome ([Anthropic](https://www.anthropic.com/news/prompt-injection-defenses)), and Shortwave.
Microsoft's stack is system prompts, Spotlighting, Prompt Shields classifiers, and deterministic blocking of exfiltration channels ([MSRC](https://www.microsoft.com/en-us/msrc/blog/2025/07/how-microsoft-defends-against-indirect-prompt-injection-attacks)).
Microsoft Defender for Office 365 now quarantines prompt-injection email with LLM classification; it analyzes "hidden, invisible, or off-screen text" as input and treats "evasion techniques such as hidden text" as a signal ([Microsoft Learn](https://learn.microsoft.com/en-us/defender-office-365/step-by-step-guides/prompt-injection-protection-defender-for-office-365)).
Google's Workspace help says flagged messages are excluded from Gemini summaries, which is a fail-closed classifier, not a stripper ([Google Workspace](https://knowledge.workspace.google.com/admin/security/how-google-helps-protect-gemini-users-from-malicious-content-and-prompt-injections)).

Output and egress fixes: EchoLeak, CamoLeak, ForcedLeak, Gemini Trifecta, and Google's external-image sanitizer.
These close exfiltration paths and do not depend on finding the injection.

Stripping fixes: only Unicode tag removal, in M365 Copilot input, ChatGPT, the OpenAI API, and Azure OpenAI ([Ars Technica](https://arstechnica.com/security/2024/10/ai-chatbots-can-read-and-write-invisible-text-creating-an-ideal-covert-channel/)).
Microsoft recommends the same normalization before AI ingestion ([Microsoft](https://www.microsoft.com/en-us/security/blog/2026/09/03/ascii-smuggling-crosses-over-from-ai-prompt-injection-to-phishing-evasion/)).
Researchers (0DIN, Radware, Immersive, Forcepoint) recommend stripping CSS-hidden text, but this survey found no vendor that said it shipped CSS-hidden-text stripping as a fix.
Radware adds that sanitization is "far less effective" against an agent manipulated by visible social engineering.

## What this means for agentmailguard

These are options for a maintainer decision, not changes made in this review.

1. Keep the existing Unicode stripping and `dropTags` list; they cover the stripping fix that vendors did adopt.
2. Keep inline-style hiding checks on the element itself: `display`, `visibility`, `opacity`, `font-size`, zero or 1px `width`/`height`/`max-width`/`max-height` with `overflow:hidden`, `mso-hide`, off-screen offsets, and the `hidden` attribute.
3. Treat colour checks as optional: every published email payload also used a size, opacity, or display property, so dropping colour logic would not have missed any of them.
4. Selector matching on `<style>` rules and full cascade inheritance did not catch anything in the email record; text they would hide still reaches Jev, which fails closed.
5. Check which MIME part each provider returns; a hidden `text/plain` alternative is a cheap, observed carrier.
6. Consider telling Jev and the agent that hidden content was removed, because flattening removes the cue that models use ([arXiv 2604.27202](https://arxiv.org/html/2604.27202v1)); this changes the visible-text contract and needs approval.
7. The main reason to keep any hiding filter is consistency: the agent should not act on text the user never saw, as in the Forcepoint and 0DIN summary-manipulation cases.

## Limits

Several disclosures did not publish markup (ShadowLeak, Permiso, OpenAI Atlas, Anthropic), so their concealment details may be incomplete.
The web telemetry counts may not transfer to email, where clients and APIs drop or rewrite some CSS.
Vendor fixes are often undisclosed; Zenity's "classifier" description of the Copilot Studio fix is the researchers' inference.
Some secondary sources were used for dates and vendor quotes where the primary post lacked them; each is linked in place.
