# General Engineering Guidelines & Agent Rules of Engagement

Universal standards and operational protocols for AI agents and engineers working across this repository.

---

## 1. Engineering Philosophy

- **Simplicity & Predictability:** Write code that is easy to understand, easy to modify, and easy to delete.
- **Explicit Over Clever:** Prefer explicit verbosity and clarity over magic, dense one-liners, or dogmatic DRYing.
- **Pragmatic Abstraction:** A little repetition is far better than a premature or over-engineered abstraction. Avoid creating layers (adapters, managers, factories) before there is a clear, multi-use need.
- **Defensive & Transparent:** Handle errors explicitly and locally; never swallow errors or invent speculative catch-all handling.

---

## 2. Non-Negotiable Testing Guardrails

- **Every Fix, Every Feature:** Every code modification, bug fix, or new feature MUST include automated tests. Do not ask whether to write tests—write them as an integral part of the change.
- **Test Error Paths First:** Do not test only the happy path. Explicitly cover edge cases, zero/nil values, and failure branches.
- **Table-Driven Tests:** Prefer structured, table-driven tests (e.g., parameterised test suites or test case tables) for business logic, parsing, and data transformations.
- **Stubbing Over Mockery:** Prefer fakes, in-memory stubs, or state assertions over brittle call-count and sequence verifications, unless call sequence is the core behavior under test.
- **Integration Tests for Boundaries:** When modifying interactions with external systems (database, object store, queues, network APIs), verify the wiring with targeted integration tests using existing test fixtures.

---

## 3. Implementation Style & Refactoring Guardrails

- **No Surgical Laziness:** When fixing a bug or adding a feature, do not apply a brittle one-line patch that leaves technical debt or messy hot-fixes behind. Understand the surrounding execution flow.
- **Leave Code Cleaner (Boy Scout Rule):** If code in the touched file is unidiomatic, poorly formatted, or outdated, clean and modernize it within the scope of your change.
- **Constrain the Blast Radius:** Keep refactorings local, scoped, and pragmatic. Do not rewrite unrelated subsystems, alter stable public contracts unnecessarily, or reformat untouched files under the guise of refactoring.

---

## 4. Review Gates & PR Protocol

- **No Unattended PRs:** The AI agent must NEVER push or open a "ready for review" pull request autonomously without explicit engineer sign-off.
- **Pre-PR Self-Verification (Agent Checklist):**
  - Run the relevant test suite and verify all touched packages pass cleanly.
  - Inspect `git diff` to eliminate lingering debug logs/prints, commented-out dead code, or unintentional whitespace noise.
  - Provide a concise summary of changes, rationale, and touched files to the engineer.
- **Pre-PR Review Prompt:** Always pause and prompt the engineer before creating a PR:
  > *"Changes and tests are complete. Please review the diff locally, or confirm if you'd like me to open a Draft PR."*
- **Draft PR by Default:**
  - When opening a pull request, ALWAYS create it as a **Draft PR** (e.g. `gh pr create --draft`).
  - **Link Linear Tickets:** Always link relevant Linear issue(s). Extract the ticket ID from the branch name (e.g. `alu-123`), commit history, or conversation context, and reference it in the PR description header (e.g. `Fixes ALU-123` or `Ref: [ALU-123](https://linear.app/...)`). If no ticket was provided, prompt the engineer for the Linear ticket ID before opening the PR.
  - **Human Verification Gate:** PR descriptions must include a clear summary, test plan, the Linear reference, and an unchecked human-verification checkbox:
    ```markdown
    - [ ] Human Verified: I have reviewed the diff and confirmed it meets our architecture and quality standards before marking ready for review.
    ```
  - Only the human engineer unmarks the PR as draft.
