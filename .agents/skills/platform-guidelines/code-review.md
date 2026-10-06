# Adversarial Code Review Guidelines

Standards and protocols for conducting thorough, adversarial code reviews on code and pull requests (pre-PR diffs, local branches, or open PRs) across the Aluma Platform monorepo.

---

## 1. Purpose & Philosophy

- **Adversarial Mindset, Direct & Objective**: Assume edge cases happen in production, APIs fail, payloads hit limits, and concurrency exposes race conditions. Deliver clear, factual engineering critiques without conversational padding or pleasantries.
- **Architectural Scope + Fine Details**: Look past trivial style. Scrutinize system boundaries, resource lifecycles, distributed state/determinism, error propagation, and local code quality.
- **Non-Exhaustive by Design**: The checklist and lenses below illustrate common failure modes but are not an exhaustive ceiling. Do not limit review scrutiny to only what is explicitly listed here—actively investigate whatever smells, edge cases, domain risks, security flaws, or operational hazards the specific change exposes.
- **Safe Fallbacks Over Scope Creep**: When reviewing optimizations or fast paths, prefer safely falling back to the standard path for unhandled edge cases rather than demanding full implementation of obscure specs.

---

## 2. Review Protocol & Checklist

### Step 1: Context & Prior Art
1. **Understand Intent & Acceptance Criteria**: Check the Linear ticket (`plugin-linear-linear`) for incident context, logs, and acceptance criteria.
2. **Diff & Stacking**: Inspect the full diff (local branch, staged changes, or PR diff). Verify if the work is part of a stack or superseded by merged changes.
3. **Check Prior Art**: Verify existing codebase patterns before flagging idioms or conventions as non-standard.

### Step 2: Technical Analysis Lenses (Non-Exhaustive)
Scrutinize the code wherever the risk lies, including but not limited to:
- **Correctness & Root Cause**: Does it solve the root cause or mask a symptom? Does it meet the ticket's acceptance criteria?
- **Testing**: Are error paths, nil/zero values, empty slices, and boundary limits tested—not just the happy path? Do test mocks match production behavior?
- **Scale & Resource Lifecycle**: Are external API limits, memory buffering (`io.ReadAll` vs streaming), and cleanup (`defer`) handled safely?
- **Distributed State & Retries**: Are Temporal workflows deterministic? Does error handling differentiate permanent failures from retryable transient ones?

### Step 3: Drafting & Submitting the Review
- **Placement**: For PR reviews, use batched inline comments on diff lines for targeted feedback and the top-level review body for architectural synthesis and overall verdict. For pre-PR local reviews, deliver a structured summary directly with exact file and line references.
- **Structure**:
  1. **Summary**: Context and verdict in 1–2 sentences.
  2. **Blockers & Production Risks**: Data corruption, deadlocks, race conditions, OOMs, breaking migrations.
  3. **Defensive Fallbacks & Scope Gaps**: Unhandled edge cases needing safe fallback guards.
  4. **Recommendations & Nits**: Concrete fixes/snippets, naming, log consistency.
  5. **Good Bits**: Highlight clean solutions or thorough tests.
- **Sign-off**: End comments and reviews as `<username>Bot` (e.g. `— PhilBot`).
