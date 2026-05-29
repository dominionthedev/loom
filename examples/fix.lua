-- fix.lua
-- Loom workflow: analyse failures, plan a fix, implement it, verify.
-- Demonstrates: agents, scope, policy, rule, use(), tasks,
-- checkpoints, worktrees, memory, export/depends_on, guard, review

-- ── Environment setup ──────────────────────────────────────────────────────

local model = "gemma4:32b"

local dev_agent = agent("dev", {
    model  = model,
    system = "You are an experienced developer. Be precise. Minimal changes only.",
    think  = "medium"
})

local planner_agent = agent("planner", {
    model  = model,
    system = "You produce structured, actionable implementation plans.",
    think  = "high"
})

local reviewer_agent = agent("reviewer", {
    model  = model,
    system = "You review plans critically. Flag ambiguities and risks.",
    think  = "medium"
})

-- Scope: what the agent can see and touch
local impl_scope = scope("implementation", {
    include = {
        "**/*.go",
        "**/*_test.go",
        "go.mod",
        "go.sum"
    },
    exclude = {
        ".git",
        ".loom/**",
        gitignored
    },
    capabilities = {
        "filesystem.read",
        "filesystem.write",
        "filesystem.glob",
        "process.execute",
        "model.think"
    }
})

-- Policies: restrict what execute() can do
local no_git = policy("no-git", {
    kind   = "deny",
    target = "process.execute",
    match  = { "git *" }
})

local no_mod = policy("no-go-mod", {
    kind   = "deny",
    target = "process.execute",
    match  = { "go mod *", "go get *" }
})

-- Rules: behavioral constraints on the agent
local dev_rules = rule("dev-rules", {
    "do not modify go.sum or go.mod",
    "do not add new dependencies",
    "minimal changes — only what is necessary to fix the issue",
    "preserve existing code style and conventions",
})

-- Notification script on failure
script("on-fail-notify", {
    source  = ".loom/scripts/notify.lua",
    trigger = "on_failure",
    require = "low"
})

-- Wire the environment together
memory({
    persist  = true,
    location = "local"
})

use("defaults", {
    agent    = dev_agent,
    scope    = impl_scope,
    policies = { no_git, no_mod },
    rules    = { dev_rules }
})

-- ── Task 1: Analyse ────────────────────────────────────────────────────────

task("analyse", {

    step("run-tests",
        execute("go test ./... 2>&1"),
        export()
    ),

    step("read-source",
        glob("**/*.go"),
        read(),
        depends_on("run-tests"),
        export()
    ),

    step("analyse-failures",
        think("medium"),
        reason(
            "Analyse the test output and source code. " ..
            "Identify the root cause of each failure. " ..
            "Note affected files and functions."
        ),
        read(),
        depends_on("read-source"),
        export()
    )

})

-- ── Task 2: Plan ───────────────────────────────────────────────────────────

-- Switch to planner agent for this task
use({
    agent = planner_agent
})

task("plan", {

    step("create-plan",
        think("high"),
        plan(
            "Based on the analysis, create a precise implementation plan. " ..
            "List each change: file, function, what to change and why. " ..
            "Order changes by dependency."
        ),
        write(artifacts),
        depends_on("analyse-failures"),
        artifacts("fix-plan.md"),
        export()
    ),

    step("review-plan",
        review("reviewer"),       -- reviewer agent critiques the plan
        artifacts("fix-plan.md"), -- reference the plan
        guard("important"),       -- password/touchid before continuing
        export()
    )

})

-- Checkpoint: create worktree before touching the project
checkpoint("before-implementation", {
    type   = worktree,
    review = "artifacts/fix-plan.md"
})

-- ── Task 3: Implement ──────────────────────────────────────────────────────

use({
    agent = dev_agent
})

task("implement", {

    step("apply-fix",
        think("medium"),
        artifacts("fix-plan.md"), -- load the plan into context
        reason("Implement the fixes from the plan. Follow it precisely."),
        all_capabilities(),       -- full access to scope capabilities
        depends_on("review-plan"),
        guard("important"),
        export()
    ),

    step("run-tests-after",
        execute("go test ./... 2>&1"),
        execute("go vet ./... 2>&1"),
        depends_on("apply-fix"),
        export()
    ),

    step("build-check",
        execute("go build ./... 2>&1"),
        depends_on("run-tests-after"),
        export()
    )

})

-- ── Task 4: Verify ─────────────────────────────────────────────────────────

-- Fresh context: verify agent has no memory of implementation
-- It can't hide problems it introduced
clean("ignore_artifacts", {
    new_session  = true,
    session_name = "verify"
})

local verify_scope = scope("verify", {
    include = {
        all_files
    },
    exclude = {
        gitignored
    },
    capabilities = {
        "filesystem.read",
        "filesystem.glob",
        "process.execute",
        "model.think"
    }
})

use({
    agent = dev_agent,
    scope = verify_scope
})

task("verify", {

    step("test",
        execute("go test ./... 2>&1"),
        execute("go vet ./... 2>&1"),
        export()
    ),

    step("analyse-results",
        think("medium"),
        artifacts("fix-plan.md"), -- check against original plan
        reason(
            "Review the test results. " ..
            "Did all previously failing tests pass? " ..
            "Were any new failures introduced? " ..
            "Did the implementation follow the plan?"
        ),
        read("**/*.go"),
        depends_on("test"),
        artifacts("verification.md"),
        export()
    ),

    step("final-review",
        review(), -- present results for manual approval
        artifacts("verification.md"),
        guard("low")
    )

})

finish() -- show summary, discipline agent report, any notifications
clear()  -- clean task residue (not artifacts, checkpoints, worktrees)
