# Golem

An autonomous coding agent with **unlimited fractal memory** and **hash-anchored editing** — built on Google's ADK v2, in Go.

Golem runs in your terminal (TUI or plain readline) and in a browser (realtime Web UI), reads and writes your code, runs shell commands, and keeps working across sessions without forgetting what came before.

Most agents hit a wall in two places: their context window fills up, and their file edits land on the wrong line. Golem is built around solving exactly those two problems.

---

## The two things that make Golem different

### 1. Unlimited fractal memory


Golem doesn't truncate or "forget" old conversation. It compresses it into a **binary tree of summaries** that can be expanded back toward the original text on demand.

- Every message is an append-only **event** with a stable id.
- Events are folded into a **binary summary tree**: level 0 covers one message, level 1 covers two, level 2 covers four, and so on.
- A node is addressed as **`id+n`** — the starting message id and the number of messages it spans (e.g. `2184+8` covers messages 2184–2191).
- The agent's context always contains a **budgeted view** of the tree, plus the most recent messages verbatim in a **live head**.
- When the model needs detail, it calls **`zoom(id, n)`** to unpack a node into its two children — drilling down one level at a time. `zoom(id, 1)` returns the exact original message, verbatim.

```text
<chat>
2048+4|user: let's wire the scanner to the UI; tool: shell go build ./...; echo: ok; talk: build is clean, adding the endpoint next
2052+4|(not summarized yet: zoom it)
</chat>
```

This means the agent can keep *working* with a small, cheap context window while retaining the *ability* to recover any past detail. There is no hard ceiling on how far back it can reach.

A few details that matter:

- **Nodes are capped at 512 bytes.** To keep summaries physically honest, the compactor hands the model a real 512-byte calibration line so it has a concrete sense of "how big is 512 bytes" — models can't count bytes, so this grounds the budget.
- **The live head is verbatim.** Recent turns are never lossy.
- **Summaries are produced in the background** by a dedicated compactor, optionally on its own model/provider (`--compactor-model`, `--compactor-provider`) so compression never competes with your main model.
- **`remember(text, scope)`** promotes an important fact — a preference, a convention, an architectural decision — into durable profile memory so it survives across sessions.
- **Mounted memories.** A session can mount additional read-only fractal stores (e.g. a `work` profile or a shared reference corpus) alongside its primary workspace memory; `zoom` and `date` accept an optional `target` to read them.

Storage is a single embedded [bbolt](https://github.com/etcd-io/bbolt) file (`fractal.db`), append-only and local-first. Legacy `events.jsonl` / `tree.jsonl` stores are migrated automatically on open.

### 2. Hash-anchored editing (hashline)

The classic agent failure is the **stale edit**: the model reads a file, the file shifts, and the patch lands on the wrong lines — or the model simply hallucinates a line number.

Hashline fixes this by making every line addressable by its **content**, not just its position.

- **`hashline_read`** returns the file with stable anchors: `42:a3` means line 42, whose trimmed content hashes to `a3` (2-char xxHash).
- **`hashline_find_block`** locates the enclosing syntactic block — function, struct, if/for — around an anchor. It understands brace languages (Go, Rust, JS/TS, Java, C/C++, C#, Kotlin, Swift, Scala, Dart, Zig, Objective-C), Python-style indentation, and Ruby blocks, and it ignores braces inside strings and comments.
- **`hashline_patch`** applies an edit bound to a verified anchor. Supported ops:

  | Op | Meaning |
  | --- | --- |
  | `SWAP` / `DEL` | replace / delete a single anchored line |
  | `INS.PRE` / `INS.POST` | insert before / after an anchored line |
  | `SWAP.BLK` / `DEL.BLK` | replace / delete a whole syntactic block |
  | `INS.BLK.PRE` / `INS.BLK.POST` | insert before / after a block |

Anchors can be a single line (`42:a3`), a range (`42:a3..45:f1`), or a block anchor. **If the content changed since the read, the patch is rejected as stale** instead of being applied to the wrong place. That turns "the agent edited the wrong line" from a routine annoyance into a near-impossible one.

`read_file`, `write_file`, and `replace_regex` are still available for cases where a plain operation is the right tool — but for careful edits, the hashline path is the safe default.

---

## Swarm

Golem agents can talk to each other. Every agent — the main orchestrator and each subagent it spawns — has an identity on a shared in-memory **swarm bus**, so a run isn't one model working alone but a small team of agents that can message, broadcast, and coordinate.

- **`send_message`** delivers point-to-point mail to a peer's mailbox. Every live agent is *registered* on the bus, so delivery to a running peer is guaranteed; a message to an unknown/offline agent is parked in a bounded dead-letter queue and the sender is told it was undeliverable, rather than it vanishing silently.
- **`broadcast_message`** publishes to a topic channel (e.g. `discoveries`, `warnings`) *and* fans the message out to every registered agent's mailbox, so a peer sees it even without subscribing.
- **`check_inbox`** drains your mailbox. Mail is *not* pushed into the model's context automatically — the orchestrator instead sees a lightweight *"N unread swarm messages"* nudge and decides when to read.
- **`claim_resource` / `release_resource` / `list_locks`** take advisory locks on a file or shared resource. The locks are **enforced**: a mutating tool (`write_file`, `replace_regex`, `hashline_patch`) that targets a resource another agent holds is refused *before* it runs, and claim/release events stream to the cockpit.

`invoke_subagent` returns its subagent's swarm id in the result header (`id: subagent-coder-…`), so the orchestrator can address a peer directly. Subagents register on the bus for the lifetime of their run, share the same fractal memory, and get the swarm tools plus their own id in their system prompt.

The Web UI cockpit renders the live agent hierarchy, the message traffic between agents, and the current advisory locks as they happen.

> Swarm is young. Each `invoke_subagent` is a blocking tool call, but ADK fans out *parallel* tool calls concurrently — so several `invoke_subagent` calls issued in one turn run as a real fan-out and join. Locks are enforced at the file-edit tool layer, but they remain advisory against arbitrary `shell`.

---

## Everything else

**Agent loop**
- Built on **Google ADK v2** with Gemini as the default model.
- Streaming responses, tool-call streaming, retry with backoff on `503`/`429`.
- A compact system prompt that teaches the model its own memory and editing primitives.

**Subagents & delegation**
- `invoke_subagent` spawns isolated subagents with their own context windows, so exploration and heavy work don't bloat the main conversation.
- Archetypes: **`researcher`** (read-only), **`coder`** (read + write + shell), **`general`**.
- Subagents share the same fractal memory and can `zoom`/`date`/`remember` too.
- Optionally run a subagent on a specific model (e.g. a cheaper or stronger one).


**Search & shell**
- `find_files` and `grep_files` — a fast pure-Go file index and SIMD/regex content search.
- `shell` — run commands, capture stdout/stderr and exit code.

**Interfaces**
- **TUI** (Bubble Tea): streaming chat, collapsible tool-call cards (`Ctrl+O`), memory-tree explorer (`Ctrl+T`), shortcuts popup (`Ctrl+K`), MCP panel, profile switcher, slash commands. Colours come from your terminal's own palette by default (`-theme nord` for the bundled palette), the wheel scroll rate is rate-limited so free-spinning mice stay readable, and the conversation opens on the newest output (`End` jumps there, `Home` to the top, `PgUp`/`PgDn` page). `Ctrl+G` hands the mouse to tmux/your terminal for copy-mode and native selection; golem keeps the wheel for its own scrolling even inside tmux, because a full-screen app leaves the pane no scrollback for copy-mode to reveal.
- **Images in the TUI**: `Ctrl+V` grabs a clipboard image — golem reads the clipboard itself (Wayland `wl-paste`, X11 `xclip`, macOS `pngpaste`), so tmux/terminal paste bindings don't matter — and dropping or typing an image path (`/tmp/shot.png`) attaches that file instead. `Ctrl+X` clears the queue. Attached images ride along with that turn as inline data for vision models; the transcript keeps the text chip (`[img 1: shot.png 1440x900 212KB]`) and the bytes are copied to `<memory-dir>/attachments/`, because fractal memory is a text log. The model is asked to say what it took from each image, so the picture stays recoverable from the text transcript after a restart.
- **Readline CLI** for piping and non-interactive use.
- **Web UI cockpit** (`-serve` / `-web`): parallel session tabs over WebSocket, swarm hierarchy view, and a fractal memory explorer. The SvelteKit SPA is embedded into the Go binary.

**Models & providers**
- Gemini (default), plus any **OpenAI-compatible** endpoint: OpenCode Go, OpenAI, OpenRouter, or a local server via `--base-url`.
- Dedicated provider/model for the background compactor.

**MCP**
- Loads MCP servers from `.golem/mcp.json` (workspace) or `~/.golem/mcp.json` (user), supporting local stdio and remote HTTP/SSE servers; their tools are exposed to the agent and its subagents.

**Profiles**
- Named profiles (`work`, `personal`, …) each with their own provider, model, memory directory, mounted memories, and compactor settings. Switch per-session or at startup.

**Import past sessions**
- `--import pick|latest|<path|id>` brings history from **Antigravity** JSONL transcripts and **Cursor** composer data into fractal memory, so the agent starts with your prior context.

---

## Install

Requires **Go 1.26+**.

```bash
git clone git@github.com:phildrip/golem.git
cd golem
go build -o bin/golem ./cmd/golem-cli
```

## Quick start

```bash
# Set a Gemini key (or configure a provider via config file)
export GEMINI_API_KEY=...

# Interactive TUI
./bin/golem

# Plain readline (no TUI)
./bin/golem -no-tui

# Web UI cockpit
./bin/golem -serve -port 8080
```

Then just talk to it. Ask it to read a file, change some code, run the tests.

## Configuration

Config is JSON, merged from lowest to highest priority:

1. Built-in defaults
2. `~/.golem/config.json` (user) — or `$GOLEM_CONFIG_DIR`
3. `.golem/config.json` (workspace)
4. Command-line flags

Example `~/.golem/config.json`:

```json
{
  "active_profile": "personal",
  "profiles": {
    "personal": {
      "provider": "gemini",
      "model": "gemini-flash-latest",
      "memory_dir": "/home/me/.golem/profiles/personal/memory",
      "mounted_memories": [
        { "name": "notes", "path": "/home/me/notes/memory", "read_only": true }
      ],
      "compactor": {
        "provider": "opencode",
        "model": "deepseek-chat"
      }
    }
  }
}
```

### Common flags

| Flag | Purpose |
| --- | --- |
| `-profile <name>` | Use a named profile |
| `-provider <gemini\|opencode\|openai\|openrouter>` | Select the model provider |
| `-model <name>` | Override the model |
| `-base-url <url>` | Custom OpenAI-compatible endpoint |
| `-api-key <key>` | Provider API key |
| `-memory-dir <dir>` | Where the fractal store lives |
| `-config <path>` | Explicit config file |
| `-compactor-model` / `-compactor-provider` | Dedicated background compaction model |
| `-import pick\|latest\|<id>` | Import past session history |
| `-no-tui` / `-tui` | Force plain CLI / force TUI |
| `-serve` / `-web` / `-port <n>` | Web UI cockpit (default port 8080) |
| `-no-mouse` | Start with in-TUI mouse capture off (restores native terminal/tmux text selection; toggle at runtime with `Ctrl+G`) |
| `-mouse` | Explicitly keep in-TUI mouse capture on, including inside tmux/screen (this is already the default; scroll keys always work regardless) |
| `-theme terminal\|nord` | TUI palette: `terminal` (default, inherits your terminal's colours and light/dark mode) or `nord` (bundled, adaptive). Also readable from `$GOLEM_THEME` |

## Architecture

```text
cmd/golem-cli     CLI entrypoint, flag parsing, TUI/readline/web selection
pkg/golem         agent orchestration, system prompt, tool wiring, memory injection
pkg/memory/fractal  append-only event store, binary summary tree, view, compactor,
                    zoom/date/remember tools, multi-store mounting
pkg/tools         shell, file editor, gg search, hashline (read/find_block/patch)
pkg/subagent      isolated subagent runner + invoke_subagent tool
pkg/swarm         message bus, mailboxes, advisory resource locks
pkg/provider      OpenAI-compatible LLM transport
pkg/mcp           MCP client/manager/toolsets
pkg/importer      Antigravity + Cursor history import
pkg/config        config file, profiles, key resolution
pkg/server        WebSocket hub, session manager, REST API, embedded SPA
pkg/tui           Bubble Tea interface
ui/               SvelteKit cockpit (embedded into the binary at build time)
```

## Development

Requires Go 1.26+ and (for the web UI) Node.

```bash
go build ./...            # compile everything
go test ./...             # full test suite
go test -race ./pkg/golem/... ./pkg/swarm/... ./pkg/memory/...   # concurrency paths

cd ui && npm install && npm run build   # rebuild the embedded SPA (copied to pkg/server/ui_dist)
```

Work is tracked with [beads](https://github.com/steveyegge/beads) (`bd`), not markdown TODO lists:

```bash
bd ready          # unblocked work
bd show <id>      # details
bd create "..."   # file a bead
bd close <id>     # done
```


## Status

Golem is under active development. The memory tree, hashline editor, and agent loop are stable and tested (all packages green, race-clean on the concurrency-heavy paths). Interfaces and config keys may still shift.

## License

Not yet specified.
