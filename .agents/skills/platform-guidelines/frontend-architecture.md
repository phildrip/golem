# Frontend Architecture Guidelines (TypeScript & Svelte)

Architecture patterns, reactivity guardrails, and data flow standards for Svelte applications across this platform.

---

## 1. Version Detection & Boundary Discipline

Our platform maintains both **Svelte 4** (legacy) and **Svelte 5** (runes) codebases.

- **Detect First:** Always check the project's `package.json` and existing component conventions before writing code:
  - **Svelte 5**: Uses runes (`$props()`, `$state()`, `$derived()`, `$effect()`).
  - **Svelte 4**: Uses legacy declarations (`export let`, `$:`, `createEventDispatcher`).
- **Never Mix Paradigms:** Do not introduce Svelte 4 syntax into Svelte 5 rune components, and do not introduce runes into unmigrated Svelte 4 apps without an intentional project upgrade.

---

## 2. Universal Frontend Principles

- **Data Flow:** Enforce "props down, events/callbacks up." Keep communication paths explicit and visible in the template hierarchy.
- **Context Discipline:** Avoid using Svelte Context as a global state dumping ground. Use contexts strictly for scoped tree dependencies (e.g. form field registration, theme providers, layout shells).
- **Controller / Service Extraction:**
  - Components should focus on presentation and markup.
  - When component state orchestration, validation, or API coordination grows beyond simple UI interactions, extract the logic into a dedicated TypeScript controller or domain service module (`.svelte.ts` in Svelte 5, or `.ts` service/store in Svelte 4).
  - Avoid creating "god components" where hundreds of lines of business logic sit in the `<script>` tag.

---

## 3. Svelte 5 Standards (Runes Mode)

- **Props & Events:**
  - Define props with typed `$props()`.
  - Pass callback props for upward communication (`onSelect: (item: Item) => void`, `onChange: (value: string) => void`) rather than using legacy event dispatchers.
- **State & Computations:**
  - Use `$state()` for local reactive state.
  - Use `$derived()` for simple expressions and `$derived.by(() => { ... })` for multi-statement computed values.
- **Strict Guardrails on `$effect()`:**
  - **Do NOT abuse `$effect()` for state synchronization.** If value `B` depends on value `A`, use `$derived()`, or update `B` inside the explicit user event handler that changed `A`.
  - Restrict `$effect()` strictly to real external side-effects: DOM measurements/focus, manual Canvas/WebGL operations, syncing with external non-Svelte third-party libraries, or logging.

---

## 4. Svelte 4 Standards (Legacy Mode)

- **Props & Event Dispatching:**
  - Declare props with `export let propName`.
  - Use `createEventDispatcher` (with typed events) or forwarded `on:event` directives for parent communication.
- **Reactivity Guardrails (`$:`):**
  - Keep reactive blocks (`$: ...`) minimal, pure, and one-directional.
  - **Avoid Cascading Chains:** Do not create deep reactive dependency chains (e.g. `$: b = a + 1; $: c = b * 2; $: d = format(c)`). Group related logic or compute via explicit pure functions.
  - **No Asynchronous Side-Effects in `$:` Blocks:** Do not trigger network requests or complex async side-effects inside reactive declarations. Trigger side-effects from explicit user interaction handlers or lifecycle hooks (`onMount`).
