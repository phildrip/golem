# Backend Architecture Guidelines (Go)

Architecture patterns, idioms, and performance standards for Go backend services across this platform.

---

## 1. Core Go Philosophy & Testing

- **Emulate the Go Standard Library:** Keep code explicit, readable, and predictable.
- **Idiomatic Table-Driven Tests:** Standardize on Go table-driven tests (`tests := []struct{ name string, ... }` run with `t.Run(tc.name, func(t *testing.T) { ... })`) for logic, parsers, and state transitions.
- **Stubbing with Pegomock (`github.com/petergtz/pegomock/v4`):**
  - Use `pegomock` for **stubbing** dependencies rather than complex custom mocks.
  - Set up behavior with `pegomock.When(...)`.
  - Use modern Go generic matchers: `pegomock.Any[T]()` (e.g. `pegomock.Any[context.Context]()`), rather than the deprecated `pegomock.AnyString()` / `pegomock.AnyInt()`.
  - Treat pegomock as a **stub first**: configure return values and verify outcomes by asserting on state or returned values. Only use `VerifyWasCalledOnce()` or call verification when interaction sequence itself is the core behavior being tested.
  - Stubs are generated from interfaces via `go generate ./...` (`//go:generate go tool pegomock generate --output-dir=mocks InterfaceName`).
- **Small, Focused Packages:** Organize packages by domain responsibility, avoiding generic catch-all packages like `utils`, `helpers`, or `common`.
- **Sensible Zero Values:** Design structs so their zero value is immediately useful or represents a safe default state.

---

## 2. Error Handling (`github.com/pkg/errors`)

Debuggability in production requires rich error context and stack traces.

- **Standard Import:** Always use `"github.com/pkg/errors"`.
- **Wrap at Boundaries:** When an error crosses a package, subsystem, or third-party/stdlib boundary, wrap it with context using `errors.Wrap(err, "action description")` or `errors.Wrapf(err, "format", ...)`.
- **Preserve Stacks:** When returning an error from stdlib or an external package that lacks a stack trace, but no additional context is needed, capture the stack with `errors.WithStack(err)`.
- **Error Origin Sites:** Use `errors.New` or `errors.Errorf` at the source of an error so the initial stack frame is preserved.
- **Message Quality:**
  - Keep wrap messages short, lowercase, and actionable (e.g., `"failed to fetch task"`, `"parsing blob JSON"`).
  - Do not prepend redundant prefixes like `"error:"` or `"failed to:"` redundantly up the chain.
  - Avoid repetitive double-wrapping within the same local boundary (e.g., wrapping in both the helper and the direct caller without new information).

---

## 3. API & Interface Design

- **Small, Composable Interfaces:** Keep interfaces small (1–3 methods max is the aim). Favor standard library conventions (`io.Reader`, `io.Closer`).
- **Consumer-Defined Interfaces:** Define interfaces in the package that *consumes* the dependency, not next to the concrete implementation that provides it.
- **Accept Interfaces, Return Structs:** Functions should accept interface parameters to remain flexible and easily testable, but return concrete types.
- **Configuration & Construction:**
  - Use config structs with sensible defaults or the functional options pattern (`With...`).
  - Avoid telescoping constructors (`NewService`, `NewServiceWithFoo`, `NewServiceWithFooAndBar`) and parameter lists with many `nil` arguments.
- **Composition Over Deep Embedding:** Use composition, middleware chains, and decorator patterns to extend behavior. Avoid deeply nested struct embedding that simulates OOP inheritance.

---

## 4. Variable & Identifier Naming

Go idioms scale identifier length to the size of the scope, but readability always comes first:

- **Local Variables:** Single- or double-word variable names (e.g., `task`, `taskID`, `blobStore`, `req`, `cfg`) are ideal. They are concise without sacrificing clarity. Avoid overly verbose Java-style names (`taskDefinitionConfigurationInstance`), but do NOT reduce variables to cryptic single letters across multi-line functions.
- **Tight Scopes & Conventional Idioms:** Standard Go conventions for tight scopes are fine where universally understood: `ctx` for context, `err` for error, `i`/`k`/`v` in tight loops, and 1–2 letter struct receivers matching the type name.
- **Wider Scopes & Exported Symbols:** Use descriptive, clear names for package-level symbols, exported functions, and domain entities (`TaskTagDefinition`, `WorkflowExecutionState`).
- **Avoid Stutter:** Do not repeat the package name in the type (e.g., inside `package task`, prefer `Definition` over `TaskDefinition`, and avoid `task.NewTask()`).

---

## 5. Pragmatic Single Responsibility Principle (SRP)

- Apply SRP with nuance rather than dogma.
- If a domain coordinator/orchestrator service needs to coordinate directly between a database store and blob storage, allow it to do so directly.
- Do not invent speculative intermediate layers (e.g. repository-manager-adapter-delegator) just to achieve theoretical purity for simple, cohesive workflows.

---

## 6. Hot Paths & Data-Oriented Design (DOD)

For throughput-critical pipelines (e.g., page rendering, OCR token processing, document separation, batch imports, tight loops):

- **Memory Locality & Cache Friendliness:**
  - Prefer contiguous slices over pointer-heavy structures, linked nodes, or deeply nested pointer indirection.
  - Slices of values (`[]Item`) avoid heap pointer chasing and improve cache locality compared to slices of pointers (`[]*Item`).
- **Allocation & Garbage Collection (GC) Pressure:**
  - Pre-allocate slice capacity when the upper bound is known (`make([]T, 0, capacity)`).
  - In inner loops, reuse buffers or leverage `sync.Pool` instead of triggering runaway heap allocations.
  - Pass value structs or reuse scratch buffers to keep allocations off the heap when escape analysis allows.
- **Benchmark & Profile First:**
  - Do not introduce DOD complexity into cold orchestration code.
  - Benchmark hot paths using standard Go benchmarks (`testing.B`) and profile allocations (`go test -benchmem`, `go tool pprof`) to guide and validate data restructuring.
