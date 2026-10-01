# Prompt-injection papers: email delivery and concealment vectors

Research date: **2026-10-01**.
This review surveys primary-source papers (2023–2026) on indirect prompt injection, with a focus on email agents and document readers.
It asks which HTML and Unicode concealment techniques the literature shows attackers use, and whether removing them measurably reduces attack success.
It is not a security audit of agentmailguard and it does not change any code.
Many 2025–2026 sources are arXiv preprints without peer review; the sources table marks the venue where one exists.

## Conclusion

No paper in this survey shows that CSS-level concealment raises attack success against an LLM compared with the same instruction in visible text.
Concealment defeats the human reader, not the model: every model-facing result that succeeded did so with visible text, and the strongest email attacks use visible, socially-engineered prose (EchoLeak, ESPI, LLMail-Inject).
The literature does show that attackers actually use cheap, mechanically removable carriers: HTML comments, hidden spans and `display:none`, white text, zero-width characters, Unicode Tag characters, variation selectors, fake role/special tokens, and Base64.
Unicode invisibles (Tags, zero-width, variation selectors) matter most where a screener or classifier sees different text from the consumer; against frontier models without decoding hints they are rarely followed (≤1.1% unhinted, no tools).
Fake chat-template tokens (`<|user|>`, `<system>`, `<think>`) are the most-reported winning trick in the largest email competition and the largest agent competition; neutralising them is cheap.
The LLM judge was the strongest single defense in the email competition, and frontier-model robustness is uneven across generations and only weakly tied to capability.
No paper evaluates stylesheet-cascade hiding (class/ID selectors in `<style>`), colour-contrast hiding in email, off-screen geometry, clip/clip-path, or 1×1 images as separate variables against frontier models; that absence is itself the finding.
The 80/20 is therefore: strip Unicode invisibles and normalise (NFKC), drop `script`/`style`/`template`/comments, honour inline `display:none`/`hidden`/`visibility:hidden`, and neutralise role tokens; leave everything else to Jev and the consumer.
The full CSS cascade, contrast checks, and geometry heuristics have no measured payoff in this literature.

## Ranked list

### Cheap removals with published evidence of attacker use

1. **Unicode Tag block U+E0000–E007F and other `Cf` format characters.**
   Tags were used to smuggle instructions into Microsoft Copilot (reported by Rehberger and cited by Reverse CAPTCHA) and evade classifier guardrails in 82–90% of attempts ([2504.11168](https://arxiv.org/abs/2504.11168)).
   Against frontier models the payload is rarely followed unless the prompt explains the encoding and tools are available: no-tools, no-hints compliance was ≤1.1%, rising to 98–100% with full hints and a Python tool ([2603.00164](https://arxiv.org/abs/2603.00164)).
   The value is mostly closing a screener/consumer mismatch at negligible cost.
2. **Zero-width and bidi characters, and variation selectors (U+FE00–FE0F, U+E0100–E01EF).**
   Zero-width binary encoding is the second family in Reverse CAPTCHA; OpenAI models decoded it more readily than Tags ([2603.00164](https://arxiv.org/abs/2603.00164)).
   Optimised variation-selector suffixes reached 80–100% jailbreak and 100% prompt-injection success on open 7–13B models, but required log-probability access and were not tested on frontier APIs ([2510.05025](https://arxiv.org/abs/2510.05025)).
   Unicode normalisation cut zero-width carrier success from 23.2% to 7.8% in a web-RAG benchmark on open models ([2601.10923](https://arxiv.org/abs/2601.10923)).
   Emoji smuggling (variation selectors) evaded all six tested guardrail classifiers 100% of the time ([2504.11168](https://arxiv.org/abs/2504.11168)).
3. **NFKC normalisation (fullwidth, mathematical alphanumerics, some confusables).**
   Fullwidth and homoglyph substitutions moderately evade classifier guardrails ([2504.11168](https://arxiv.org/abs/2504.11168)) and open models ([2508.14070](https://arxiv.org/abs/2508.14070)).
   No paper measures their effect on a frontier consumer separately.
4. **Fake role and special tokens.**
   LLMail-Inject's top teams most often used `<|user|>`, `<|assistant|>`, `<|end|>`, fake tool-output delimiters, and HTML-like tags ([2506.09956](https://arxiv.org/abs/2506.09956)).
   The organisers added special-token sanitisation in phase 2; end-to-end success fell from 0.8% to 0.3% of submissions, alongside other defense changes, so the token filter's isolated effect is not measured.
   The Gray Swan/UK AISI agent competition lists system-prompt override tags, faux `<think>` reasoning, and fake session resets as the most common universal strategies ([2507.20526](https://arxiv.org/abs/2507.20526)).
5. **HTML comments, `script`, `style`, `template`.**
   Greshake et al. injected Bing Chat through HTML comments and Markdown comments that were invisible to users ([2302.12173](https://arxiv.org/abs/2302.12173)).
   Hidden HTML comments in Markdown skills influenced DeepSeek-V3.2 and GLM-4.5-Air; a defensive system prompt prevented the malicious tool calls ([2602.10498](https://arxiv.org/abs/2602.10498)).
   Removal is a node-type check, so the cost is effectively zero.
6. **Inline `display:none`, the `hidden` attribute, and inline `visibility:hidden`.**
   Hidden spans were the most successful carrier (34.0%) in the web-RAG benchmark, and sanitisation cut them to 12.3%; off-screen CSS fell from 30.1% to 9.8% ([2601.10923](https://arxiv.org/abs/2601.10923)).
   That benchmark used 8–70B open models and does not report the same payload as visible text, so it does not show that hiding beats visibility.
   PhantomLint found hidden prompts in 113 real CVs, preprints, theses, and blog posts, and catalogues the same hiding methods ([2508.17884](https://arxiv.org/abs/2508.17884)).
7. **Encodings (Base64, hex, ROT13) — screen, do not strip.**
   Greshake's Base64 injection needed an explicit decode instruction to work ([2302.12173](https://arxiv.org/abs/2302.12173)).
   LLMail-Inject's LLM annotator decoded a Base64 email body and flagged it ([2506.09956](https://arxiv.org/abs/2506.09956)).
   Base64 and hex reached 64–67% on open 3.8–32B models ([2508.14070](https://arxiv.org/abs/2508.14070)).
   Email legitimately contains encoded content, so the evidence supports letting Jev see it rather than removing it.

### Expensive machinery without published evidence

1. **Full stylesheet cascade (selector matching, specificity, inheritance).**
   No surveyed paper hides text through `<style>` class or ID rules as a distinct variable, and none reports a success-rate difference between inline and stylesheet hiding.
   The one benchmark that measures hidden HTML carriers used a DOMPurify-based sanitiser, not a cascade ([2601.10923](https://arxiv.org/abs/2601.10923)).
2. **Colour contrast (white-on-white).**
   White text is the best-documented real-world concealment, but in PDFs and manuscripts, not email ([2507.06185](https://arxiv.org/abs/2507.06185), [2508.17884](https://arxiv.org/abs/2508.17884)).
   No paper measures whether white text is followed more often than visible text; it hides the instruction from people, and an LLM reads both equally.
3. **Off-screen geometry, clip/clip-path, zero width/height, opacity thresholds, 1×1 images.**
   Only "off-screen CSS" appears as a carrier, on open models and without a visible-text control ([2601.10923](https://arxiv.org/abs/2601.10923)).
   No paper evaluates the remaining heuristics.
4. **Selector-work estimation.**
   No paper addresses it; it exists only to protect the cascade from pathological CSS.

## Findings by question

### Which delivery mechanisms do papers use?

Most benchmarks place plain visible text in a tool-result field: InjecAgent ([2403.02691](https://arxiv.org/abs/2403.02691)), BIPIA ([2312.14197](https://arxiv.org/abs/2312.14197)), AgentDojo ([2406.13352](https://arxiv.org/abs/2406.13352)), WASP ([2504.18575](https://arxiv.org/abs/2504.18575)), and the Gemini adaptive evaluation ([2505.14534](https://arxiv.org/abs/2505.14534)).
The two largest attacker datasets are text-only: LLMail-Inject accepted only subject and body text ([2506.09956](https://arxiv.org/abs/2506.09956)), and ART used chat and tool-data injections ([2507.20526](https://arxiv.org/abs/2507.20526)).
Concealment-focused work uses HTML/Markdown comments ([2302.12173](https://arxiv.org/abs/2302.12173), [2602.10498](https://arxiv.org/abs/2602.10498)), hidden HTML, ARIA, and alt text ([2601.10923](https://arxiv.org/abs/2601.10923)), Unicode invisibles ([2603.00164](https://arxiv.org/abs/2603.00164), [2510.05025](https://arxiv.org/abs/2510.05025)), malicious font glyph mappings ([2505.16957](https://arxiv.org/abs/2505.16957)), PDF hidden text ([2607.19396](https://arxiv.org/abs/2607.19396)), and optimised gibberish ([2410.14923](https://arxiv.org/abs/2410.14923)).
The only production email exploit in the literature, EchoLeak against Microsoft 365 Copilot, evaded the XPIA classifier by phrasing the email as a normal request to the human recipient; the paper describes no CSS hiding ([2509.10540](https://arxiv.org/abs/2509.10540)).

### Does success depend on concealment?

No surveyed paper runs the same payload hidden and visible against a frontier model and reports a higher success rate for hidden text.
The strongest email results come from visible text: ESPI's forged mailbox-state messages reached 73.5% average success across six 2025–2026 frontier models, against 4.6–8.1% for template baselines ([2507.02699](https://arxiv.org/abs/2507.02699)).
LLMail-Inject's successful submissions disguised intent through wording, fake email blocks, multilingual text, and character obfuscation; in the organisers' words, some successful attacks "appeared as normal-looking emails" ([2506.09956](https://arxiv.org/abs/2506.09956)).
Representation does change success for one harness: the same "hidden Unicode" channel scored 0.0% as text and 25.5% as a parsed file against deepseek-v4-flash ([2608.16393](https://arxiv.org/abs/2608.16393)).
That result supports normalising what a parser emits; it does not show that hiding from people helps attackers against the model.

### Success against frontier models when the injection arrives as a tool result

GPT-4 with ReAct followed injected tool-output instructions in 24% of cases (47% with a hacking prefix); function-calling GPT-4 fell to 6.6–7.1% ([2403.02691](https://arxiv.org/abs/2403.02691)).
In AgentDojo, more capable models were easier to attack; Claude 3.5 Sonnet had 33.9% targeted success and Claude 3 Opus 11.3%, and end-of-output injections reached up to 70% ([2406.13352](https://arxiv.org/abs/2406.13352)).
Gemini 2.5 cut adaptive email-exfiltration success to 53.6% (TAP), 40.8% (Actor-Critic), and 4.2% (Beam Search), from 99.8%, 66.2%, and 74.8% on Gemini 2.0; DeepMind states that more capable models are not necessarily more secure ([2505.14534](https://arxiv.org/abs/2505.14534)).
ART found 27.1% success for indirect injection against 5.7% for direct attacks, and limited correlation between robustness and capability or inference compute ([2507.20526](https://arxiv.org/abs/2507.20526)).
WASP agents were partly hijacked in up to 86% of cases but completed the attacker goal in only 0–17% ([2504.18575](https://arxiv.org/abs/2504.18575)).
In LLMail-Inject, emails were fed to GPT-4o-mini as tool messages; teams succeeded less often against it than against Phi-3, which the authors attribute partly to instruction-hierarchy training ([2506.09956](https://arxiv.org/abs/2506.09956)).

### Which cheap pre-filters measurably reduced success?

HTML sanitisation and Unicode normalisation together cut macro success from 24.9% to 4.7% on open models ([2601.10923](https://arxiv.org/abs/2601.10923)).
Spotlighting via datamarking or encoding cut success from over 50% to under 2% on GPT-3.5/4 ([2403.14720](https://arxiv.org/abs/2403.14720)); in LLMail-Inject it was weaker than the LLM judge (team success 0.529 against 0.318) ([2506.09956](https://arxiv.org/abs/2506.09956)).
In LLMail-Inject, the LLM judge had the highest recall (0.994 in phase 1), and all defenses combined with GPT-4o had no successful phase 2 attack ([2506.09956](https://arxiv.org/abs/2506.09956)).
An LLM rewriting tool output ("Sanitizer") reached near-zero success on four public benchmarks, but staged adaptive attacks raised it to 30% ([2510.05244](https://arxiv.org/abs/2510.05244)).
Generic prompt and LLM detectors barely reduced ESPI (73.5% to about 70%); only verifying mailbox state against the provider reached 0% ([2507.02699](https://arxiv.org/abs/2507.02699)).
Adaptive attacks broke eight published defenses with over 50% success ([2503.00061](https://arxiv.org/abs/2503.00061)), and a critical re-evaluation found StruQ, SecAlign, and Instruction Hierarchy weaker than reported ([2505.18333](https://arxiv.org/abs/2505.18333)).
Length is the only questioned pre-filter with indirect evidence: fragmented instructions failed without filler but reached 75.3% with 8k tokens of filler ([2609.36576](https://arxiv.org/abs/2609.36576)), and LLMail-Inject's authors note that TaskTracker's signal dilutes in long context ([2506.09956](https://arxiv.org/abs/2506.09956)).
No paper measures an email body length cap directly.

## Notes on the current implementation

`stripInvisible` in `internal/content/content.go` removes `Cc` and `Cf` characters, which covers the Tag block, zero-width characters, and bidi controls, plus U+E0100–E01EF; it then applies NFKC.
It does not appear to remove U+FE00–FE0F, the 16 basic variation selectors used by emoji smuggling and by 2510.05025; these are category `Mn`, not `Cf`.
`cleanText` replaces `<` and `>` with `‹` and `›`, which already defangs literal `<|user|>`-style role tokens, though bracket-free fakes like "### system" remain.
`dropTags` already removes `script`, `style`, `template`, and similar elements.

## Recovery note

The research agent that wrote this file stopped with a model refusal while emitting the final write.
The text above is the recovered draft; the "Notes on the current implementation" section was cut mid-sentence and the sources table was not emitted.
All arXiv identifiers are cited inline; verify any before relying on it.
