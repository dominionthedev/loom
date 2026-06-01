# Changelog

All notable changes to Loom are documented here.

---

## [v0.1] — 2026-06-01

Initial release.

### Added

**Core runtime**
- Lua DSL — file-as-workflow model, no wrapper function required
- `agent()`, `scope()`, `policy()`, `rule()`, `workspace()`, `memory()`, `use()` environment primitives
- `task()`, `step()` execution primitives with table syntax
- Step operations: `think()`, `reason()`, `plan()`, `read()`, `write()`, `execute()`, `background()`, `glob()`, `watch()`, `edit()`, `all_capabilities()`, `capability()`, `depends_on()`, `export()`, `artifacts()`, `guard()`, `review()`
- Task lifecycle: `checkpoint()`, `finish()`, `clear()`, `clean()`
- Built-in constants: `gitignored`, `all_files`, `artifacts`, `worktree`

**Agent system**
- Tool-calling loop — agent drives execution via `TOOL: / INPUT:` format
- `reason()` — free-form reasoning, output flows to context
- `plan()` — structured reasoning, always high thinking, output to artifacts
- Multi-line tool input support (`<<<...>>>`)
- Step history maintained across steps within a task
- Thread-safe parallel step execution

**Capability system**
- Field schema system — `SourceArg`, `SourceContext`, `SourceEither`
- Args from Lua file constrain capability fields — agent provides the rest
- Built-in: `filesystem.read`, `filesystem.write`, `filesystem.edit`, `filesystem.glob`
- Built-in: `process.execute`, `process.background`, `process.watch`
- Built-in: `web.search`, `web.fetch` (stub — v0.3)
- Built-in: `model.think`
- Capability contracts with effects declarations

**Orchestration**
- DAG execution graph within tasks — topological sort, cycle detection
- Parallel step execution at same graph depth
- Fast path for fully-specified capability-only steps (no agent needed)
- Context accumulation across steps
- `export()` / `depends_on()` context flow
- Cross-task context via artifact loading

**Guard system**
- Scope enforcement — capability must be declared in scope
- Path enforcement — filesystem paths respect include/exclude globs
- Policy enforcement — deny/allow/limit by capability + match patterns
- Step-level capability access — only declared caps are accessible

**Storage**
- Artifact persistence — `.loom/artifacts/` with `.meta.json` sidecar
- Run history — `.loom/logs/`
- State storage — `.loom/state/`

**CLI**
- `loom run <file>` — run all tasks or `--task <name>` for one
- `loom run --dry-run` — show execution plan without running
- `loom run -v / --verbose` — show tool calls, model replies, capability results
- `loom inspect <file>` — show task graph, step DAG, policies, agents
- `loom models` — list models available on the Ollama server
- `--model`, `--model-mid`, `--model-high` flags for think-level routing
- `--version` flag

**Model routing**
- `think("low")` → default model (`gemma3:4b`)
- `think("medium")` → mid model (`devstral-small-2:24b`)
- `think("high")` + `plan()` → high model (`devstral-small-2:24b`)
- Explicit model name in `think()` for per-step override

**CI + release**
- GitHub Actions CI — build, vet, test on push/PR
- GoReleaser release pipeline on tag push
- Multi-platform builds: Linux, macOS, Windows × amd64/arm64

**Examples**
- `examples/fix.lua` — full workflow: analyse → plan → implement → verify
- `examples/review.lua` — parallel code review with no-write policy
