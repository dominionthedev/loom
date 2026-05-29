# loom 🧵

> Weave your development workflow. Where execution thinks.

Loom is a programmable AI workflow runtime for development operations.

You define the structure. The runtime executes it. The agent thinks inside it.

---

## How it works

```
You write a Lua file
    ↓
Runtime evaluates it — agents, scopes, policies, rules
    ↓
Tasks execute step by step (or in parallel where independent)
    ↓
Guard enforces every capability call
    ↓
Agent reasons inside declared boundaries only
```

The file IS the workflow. No wrapper. No config file. Just Lua.

---

## Install

```bash
go install github.com/dominionthedev/loom@latest
```

Or from source:

```bash
git clone https://github.com/dominionthedev/loom
cd loom
go build -o bin/loom ./
```

---

## Quick start

```lua
-- fix.lua

local dev = agent("dev", {
    model  = "llama3.2",
    system = "You are an experienced developer. Minimal changes only.",
    think  = "medium"
})

local impl_scope = scope("implementation", {
    include      = { "**/*.go" },
    exclude      = { ".loom/**", gitignored },
    capabilities = { "process.execute", "filesystem.read", "filesystem.write" }
})

local no_git = policy("no-git", {
    kind   = "deny",
    target = "process.execute",
    match  = { "git *" }
})

use({
    agent    = dev,
    scope    = impl_scope,
    policies = { no_git }
})

task("fix", {
    step("run-tests",
        execute("go test ./... 2>&1"),
        export()
    ),
    step("analyse",
        depends_on("run-tests"),
        reason("Identify test failures and their root cause."),
        export()
    ),
    step("apply-fix",
        depends_on("analyse"),
        reason("Apply a precise fix based on the analysis."),
        all_capabilities(),
        guard("important")
    )
})

finish()
```

```bash
loom run fix.lua
loom run fix.lua --task fix
loom inspect fix.lua
loom run fix.lua --dry-run
```

---

## DSL reference

### Environment (top-level)

| Primitive | Description |
|---|---|
| `agent(name, {...})` | Define an agent with model + system prompt |
| `scope(name, {...})` | Define operational boundary — paths + capabilities |
| `policy(name, {...})` | Restrict capability usage — deny/allow/limit |
| `rule(name, {...})` | Behavioral constraints on the agent |
| `workspace(name, {...})` | Workspace configuration — dir, shell, env_file |
| `memory({...})` | Initialize memory for the session |
| `use({...})` | Wire environment — second call clears first |

### Task level

| Primitive | Description |
|---|---|
| `task(name, {...})` | Primary execution unit |
| `checkpoint(label, {...})` | Snapshot state + optional review gate |
| `finish()` | End dialogue + summary |
| `clear()` | Clean task residue |
| `clean("ignore_*", {...})` | Fresh environment + optional new session |

### Step level (in order)

```lua
step("name",
    depends_on("other-step"),     -- DAG + context import
    artifacts("existing.md"),     -- reference before reasoning
    think("low|medium|high"),     -- think level for this step
    reason("prompt"),             -- free-form reasoning → context
    plan("prompt"),               -- structured reasoning → artifact
    read("pattern" | artifacts),  -- read capability
    write("pattern" | artifacts), -- write capability
    execute("cmd"),               -- locked command
    execute(),                    -- open (policy-filtered)
    glob("pattern"),              -- glob capability
    all_capabilities(),           -- full scope access
    export(),                     -- export context downstream
    artifacts("output.md"),       -- create artifact
    guard("low|important|critical"),
    review()                      -- pause for approval
)
```

### Built-in capabilities

| Capability | Effect |
|---|---|
| `filesystem.read` | `filesystem.read` |
| `filesystem.write` | `filesystem.modify` |
| `filesystem.edit` | `filesystem.modify` |
| `filesystem.glob` | `filesystem.read` |
| `process.execute` | `process.spawn` |
| `process.background` | `process.spawn` |
| `process.watch` | `filesystem.read` |
| `web.search` | `web.read` |
| `web.fetch` | `web.read` |
| `model.think` | — |

### Built-in constants

```lua
gitignored   -- all patterns from .gitignore
all_files    -- all files in workspace
artifacts    -- .loom/artifacts/ path
worktree     -- worktree checkpoint type
```

---

## Model config

```bash
export OLLAMA_HOST=http://localhost:11434
# or
export OLLACLOUD_HOST=http://localhost:11434

loom run fix.lua --model llama3.2
```

---

## Storage

Everything lives under `.loom/`:

```
.loom/
├── artifacts/     — named outputs + .meta.json
├── logs/          — run history
├── state/         — runtime state
├── sessions/      — session data (v0.4)
└── worktrees/     — project copies (v0.3)
```

---

## License

[MIT](./LICENSE) · Built by [DominionDev](https://github.com/dominionthedev)

<p align="center">
    <a href="https://dominionthedev.github.io">
        <img src="https://raw.githubusercontent.com/dominionthedev/dominionthedev/main/assets/watermark-animated.svg" width="600" />
    </a>
</p>

