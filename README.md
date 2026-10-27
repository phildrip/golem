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

**Sandbox**
- Every tool that can touch the host — `shell`, the file tools, `hashline_patch`, `web_fetch` and `web_search` — asks one policy layer first (`pkg/sandbox`), so there is exactly one place to reason about what the agent may do.
- The default mode is `policy`: dangerous shell shapes are refused with a tool error the model can route around (`rm -rf ~`, `git checkout .`, `git reset --hard`, `git clean -fdx`, `sudo`, `curl … | sh`, `mkfs`, `dd of=/dev/…`, `chmod 777 /`, `shutdown`/`reboot`), and writes are confined to the workspace, with `allow_write` adding directories. `git push --force` and `git branch -D` come back as *confirm*; until the interactive approval gate lands that is reported as a refusal with the reason attached.
- `sandbox.mode: "isolation"` enforces the same intent with the kernel instead of with a string check. Shell commands run under [Landlock](https://landlock.io), which golem applies to itself and which survives `exec`, so the command sees only the workspace, the toolchain and the system trees, cannot write outside them, and cannot reach the network unless `network` is `"host"`. It needs no root, no user namespaces, no `bwrap`/`nsjail` and no daemon: `pkg/sandbox` re-executes the golem binary as the confinement shim, so the whole sandbox is inside the binary you built. Where the kernel has Landlock ABI 6 or newer the command also cannot signal anything outside the sandbox, so a prompt-injected model cannot kill the agent that spawned it. The banner prints the ABI and what it enforces, and a kernel without Landlock is a startup error rather than a silent downgrade to policy.
- `allow_write` adds writable directories and `read_only` adds readable ones. The defaults are `/usr`, `/bin`, `/sbin`, `/lib*`, `/etc`, `/opt`, `/usr/local` and `$GOROOT` readable; the workspace, `/tmp`, `/dev/null`, `$GOPATH`, `$GOCACHE` and the usual build caches writable. `$HOME` and `/proc` are deliberately *not* granted — `/proc` would hand the command the agent's own environment.
- `sandbox.mode: "off"` restores the pre-sandbox behaviour exactly (nothing is checked). `allow_net` optionally pins the hosts `web_fetch`/`web_search` may reach.
- The startup banner prints the effective mode, so a profile that accidentally says `off` is visible before the first tool call.

**Interfaces**
- **TUI** (Bubble Tea): streaming chat, collapsible tool-call cards (`Ctrl+O`), memory-tree explorer (`Ctrl+T`), shortcuts popup (`Ctrl+K`), MCP panel, profile switcher, slash commands with a live completion popup (`/` then `Tab`, `↑`/`↓`, `Enter`). Colours come from your terminal's own palette by default (`-theme nord` for the bundled palette), the wheel scroll rate is rate-limited so free-spinning mice stay readable, and the conversation opens on the newest output (`End` jumps there, `Home` to the top, `PgUp`/`PgDn` page). `Ctrl+G` hands the mouse to tmux/your terminal for copy-mode and native selection; golem keeps the wheel for its own scrolling even inside tmux, because a full-screen app leaves the pane no scrollback for copy-mode to reveal. The prompt starts as a single line and grows as you add to it (up to a third of the pane, then it scrolls its own draft) — `Enter` sends, `Alt+Enter` or `Ctrl+J` starts a new line.
- **Images in the TUI**: `Ctrl+V` grabs a clipboard image — golem reads the clipboard itself (Wayland `wl-paste`, X11 `xclip`, macOS `pngpaste`), so tmux/terminal paste bindings don't matter — and dropping or typing an image path (`/tmp/shot.png`) attaches that file instead. `Ctrl+X` clears the queue. Attached images ride along with that turn as inline data for vision models; the transcript keeps the text chip (`[img 1: shot.png 1440x900 212KB]`) and the bytes are copied to `<memory-dir>/attachments/`, because fractal memory is a text log. The model is asked to say what it took from each image, so the picture stays recoverable from the text transcript after a restart.
- **Readline CLI** for piping and non-interactive use.
- **Web UI cockpit** (`-serve` / `-web`): parallel session tabs over WebSocket, swarm hierarchy view, and a fractal memory explorer. The SvelteKit SPA is embedded into the Go binary.
- **Web auth**: `/api/*` and `/ws` require a bearer token (generated on first run, stored in `config.json`, overridable with `-token`). The SPA shell stays open so it can ask for the token, which it exchanges for an HttpOnly cookie — a browser cannot attach headers to a WebSocket handshake. The server binds `127.0.0.1` by default; widening it is an explicit `-host`.

**Slash commands (TUI)**
- Type `/` and the command palette opens above the prompt as you type: `Tab` completes the highlighted command, `↑`/`↓` move the highlight, `Enter` takes it, `Esc` dismisses. Once the command is complete (or arguments start) `Enter` runs it.
- `/model` opens a **model picker** with the provider's own model list (OpenAI-compatible providers are asked for `/models`; Gemini is asked for `generativelanguage.googleapis.com`). The list is fetched live, falls back to a bundled shortlist when the provider is unreachable (the modal says which), marks the current model, and switches on `Enter` — no restart, same provider, credentials and memory mounts. `↑`/`↓` (or `j`/`k`) choose, `PgUp`/`PgDn` page, `Esc`/`q` closes.
- `/model <name>` skips the picker and switches straight to a named model **after checking the name against the provider's list**: an exact match wins, a unique prefix is accepted (`/model deepseek-v4.1-f`), and anything else is refused with the closest real names (`Did you mean: …`) instead of leaving you on a model that fails mid-conversation. When the provider cannot be reached the name cannot be verified, so the switch still happens and says so.
- `/profile [name]` inspects profiles, or switches profile — and with it provider, model, memory dir and compactor settings.
- `/remember`, `/tree` (`/memory`), `/zoom <id> <n>`, `/compact`, `/clear`, `/tools`, `/mcp`, `/shortcuts`, `/help`, `/exit`.

**Models & providers**
- Gemini (default), plus any **OpenAI-compatible** endpoint: OpenCode Go, OpenAI, OpenRouter, or a local server via `--base-url`.
- Switch model at runtime from the `/model` picker (or with `/model <name>` / `-model <name>` at startup); switch provider + model + memory together with a profile.
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

# Web UI cockpit (loopback only, token auth on)
./bin/golem -serve -port 8080

# Reachable from your phone over tailscale, still only on the tailnet
./bin/golem -serve -host $(tailscale ip -4)
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
      },
      "sandbox": {
        "mode": "isolation",
        "network": "none",
        "allow_write": ["~/scratch"],
        "read_only": ["/srv/datasets"],
        "allow_net": ["example.com"]
      }
    }
  },
  "server": {
    "host": "127.0.0.1",
    "port": 8080,
    "token": "generated-on-first-serve-run",
    "allow_origins": ["https://golem.tailnet.ts.net"]
  }
}
```

`sandbox.mode` defaults to `policy` when the block is absent (see **Sandbox** above for what that denies) and `sandbox.network` defaults to `none`, because a model that can reach the network can exfiltrate whatever it can read. `server.token` is written automatically the first time you run `-serve`; delete it to get a fresh one (every authorised device will have to paste it again). `allow_origins` is only needed when a reverse proxy rewrites the `Host` header — `tailscale serve` does not.

### Common flags

| Flag | Purpose |
| --- | --- |
| `-profile <name>` | Use a named profile |
| `-provider <gemini\|opencode\|openai\|openrouter>` | Select the model provider |
| `-model <name>` | Override the model (in the TUI, `/model` picks from the provider list and switches live) |
| `-base-url <url>` | Custom OpenAI-compatible endpoint |
| `-api-key <key>` | Provider API key |
| `-memory-dir <dir>` | Where the fractal store lives |
| `-config <path>` | Explicit config file |
| `-compactor-model` / `-compactor-provider` | Dedicated background compaction model |
| `-import pick\|latest\|<id>` | Import past session history |
| `-no-tui` / `-tui` | Force plain CLI / force TUI |
| `-serve` / `-web` / `-port <n>` | Web UI cockpit (default port 8080) |
| `-host <iface>` | Interface the web server binds (default `127.0.0.1`; use your tailscale IP or `0.0.0.0` to reach it from a phone) |
| `-token <tok>` | Web auth token (default: generated once and stored in `config.json`) |
| `-no-web-auth` | Disable web auth entirely — only safe when bound to loopback |
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
