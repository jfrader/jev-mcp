---
name: jev-mcp
description: Get fast, cheap, typed judgments from TypeSafe's Jev model through the jev MCP tools — pick one of a set, score on a rubric, or answer yes/no with a calibrated probability. Use when a decision has a closed answer set and would otherwise cost reasoning tokens or sit in context: routing work to a subagent or handler, picking one of several files, grading severity, risk or relevance, triaging issues, alerts, logs or diffs, or filtering noise before it enters context. Do not use for writing code or prose, or for counting, arithmetic, or dates.
---

# Jev decisions

Jev is a **System One model**, not a language model. You send a `state` and typed
questions, and it returns typed answers with probabilities. It cannot write, explain, or
reason in steps. Use it as a decision primitive, never as a place to think.

Input tokens are billed and output tokens are free, but the larger win is context: a short
typed answer replaces a reasoning block that would otherwise be re-sent on every later
turn.

## Reach for it when

- The answer is one of a closed set you can name in advance.
- The same judgment recurs — per file, per log line, per issue, per tool call.
- You would otherwise decide in the transcript, and the decision is not the work itself.
- You want a calibrated confidence to decide whether to act or escalate.

## Do not reach for it when

- You need text, code, or an explanation out. It cannot generate.
- A deterministic check answers it: regex, path match, count, arithmetic, date order,
  version compare, exit code, schema validation. **Code owns computation.**
- You need a count. Ask one noul per candidate and tally in code.
- The question needs several hops of reasoning. Split it and combine the answers yourself.

## Which primitive

| Need | Tool | Notes |
| --- | --- | --- |
| One of a defined set | `choose` | Relative: it settles *which* option. |
| Position on an ordered rubric | `score` | 2–10 levels that each stand alone. |
| Whether a condition holds | `noul` | Absolute probability of yes. **No confidence.** |
| Several of the above at once | `ask` | **Prefer this.** One request, parallel. |

## Calling it

Prefer one batched `ask` over a call per decision: many questions run in parallel against
one state, which is far cheaper and faster than separate requests.

```text
ask    state, questions[{id,type,instructions,criteria|levels}], min_confidence
choose state, instructions, options   # object of name→description, or array of names
score  state, instructions, levels
noul   state, instructions, criteria?, threshold?
```

Write each `instructions` as one narrow, **literal** condition and put boundary cases in
the criteria. Reference nested state with backticked paths such as `` `ticket.subject` ``.
Include a "none of these" option when nothing may fit — it cannot choose an option you
never offered.

```json
{"state": {"ticket": "Payouts failing for 3 days"},
 "questions": [
   {"id":"route","type":"choice","instructions":"Which team handles this?",
    "criteria":{"billing":"payments","technical":"bugs","infra":"outages"}},
   {"id":"severity","type":"score","instructions":"How bad for the customer?",
    "levels":["cosmetic","degraded","blocked"]},
   {"id":"urgent","type":"noul","instructions":"Does this convey time pressure?"}
 ]}
```

## Confidence and escalation

`choose` and `score` return `confidence`; `ask` flags each answer with `reliable` against
`min_confidence` (0.5 by default) and lists any unreliable ids. `noul` has no confidence —
a value near 0.5 is a coin flip, not medium intensity.

Use both axes: the answer says **what**, confidence says **whether to act**. Raise the bar
with the stakes — a read-only classification can act at 0.5, while anything destructive or
user-facing should require high confidence or a person. Never present an unreliable answer
as settled, and never let a confidence threshold be the only guard on an irreversible
action.

Thresholds are per question. Do not reuse a noul threshold on a choice, and do not assume
`P(yes) + P(no) = 1` across separate questions.

## Hard limits

- **Not a calculator.** No counting, arithmetic, ordering, or date comparison. Extract with
  Jev and compute in code. Weak on hex, RGB and binary; pass named buckets instead.
- **Literal.** It answers the words you wrote. When you catch yourself explaining what you
  meant, that explanation is the missing half of the question.
- **Context rot.** Accuracy falls as `state` fills with unrelated detail. Filter first and
  send only what the question needs.
- **Steerable.** Adversarial content can move an answer. Screening untrusted input with Jev
  is defence in depth, never a security boundary.
- **English is strongest.** Test other languages and expect to escalate more.
- **Aliases move.** `jev-latest` resolves to a version that changes; responses echo the
  versioned id, so pin `TYPESAFE_MODEL` once your thresholds are tuned.
- **Calibration is group-level**, not a guarantee per answer. Keep the consequence in code.

## See also

`README.md` for setup. <https://docs.typesafe.ai> carries the current API, primitives and
cookbooks; the vendor's jaggedness notes are worth reading before trusting a new question
shape.
