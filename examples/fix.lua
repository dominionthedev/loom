-- fix.lua
-- Analyse test failures, plan a fix, implement it, verify.
-- Demonstrates: multiple tasks, checkpoint with worktree, guard, clean().
-- Run: loom run examples/fix.lua
-- Run one task: loom run examples/fix.lua --task analyse

local dev = agent("dev", {
    model  = "devstral-small-2:24b",
    system = "You are an experienced developer. Make precise, minimal changes only.",
    think  = "medium"
})

local planner = agent("planner", {
    model  = "devstral-small-2:24b",
    system = "You produce structured, actionable implementation plans.",
})

local impl_scope = scope("implementation", {
    include      = { "**/*.go", "go.mod" },
    exclude      = { ".loom/**", gitignored },
    capabilities = {
        "filesystem.read",
        "filesystem.write",
        "filesystem.glob",
        "process.execute"
    }
})

local no_git = policy("no-git", {
    kind   = "deny",
    target = "process.execute",
    match  = { "git *" }
})

local dev_rules = rule("dev-rules", {
    "do not modify go.sum or go.mod",
    "do not add new dependencies",
    "minimal changes only — fix what is broken, nothing more",
    "preserve existing code style",
})

memory({ persist = true, location = "local" })

use({
    agent    = dev,
    scope    = impl_scope,
    policies = { no_git },
    rules    = { dev_rules }
})

-- ── Task 1: Analyse ────────────────────────────────────────────────────────

task("analyse", {
    step("run-tests", {
        execute("go test ./... 2>&1"),
        export()
    }),

    step("read-source", {
        depends_on("run-tests"),
        glob("**/*.go"),
        read(),
        export()
    }),

    step("analyse-failures", {
        depends_on("read-source"),
        reason(
            "Analyse the test output and source code. " ..
            "Identify the root cause of each failure. " ..
            "Note the affected files and functions."
        ),
        write(artifacts),
        artifacts("analysis.md"),
        export()
    })
})

-- ── Task 2: Plan ───────────────────────────────────────────────────────────

use({
    agent    = planner,
    scope    = impl_scope,
    policies = { no_git },
    rules    = { dev_rules }
})

task("plan", {
    step("load-analysis", {
        artifacts("analysis.md"),
        read(artifacts),
        export()
    }),

    step("create-plan", {
        depends_on("load-analysis"),
        plan(
            "Create a precise implementation plan to fix the failures. " ..
            "List each change: file, function, exact change, and why."
        ),
        write(artifacts),
        artifacts("fix-plan.md"),
        export()
    })
})

-- Checkpoint: snapshot before touching any source files.
checkpoint("before-implementation", {
    type   = worktree,
    review = "fix-plan.md"
})

-- ── Task 3: Implement ──────────────────────────────────────────────────────

use({
    agent    = dev,
    scope    = impl_scope,
    policies = { no_git },
    rules    = { dev_rules }
})

task("implement", {
    step("load-plan", {
        artifacts("fix-plan.md"),
        read(artifacts),
        export()
    }),

    step("apply-fix", {
        depends_on("load-plan"),
        reason(
            "Implement the fixes from the plan exactly as described. " ..
            "Make only the changes listed. No extras."
        ),
        all_capabilities(),
        guard("important"),
        export()
    }),

    step("verify-build", {
        depends_on("apply-fix"),
        execute("go build ./... 2>&1"),
        execute("go test ./... 2>&1"),
        export()
    })
})

-- ── Task 4: Verify (fresh context — clean agent can't hide problems) ────────

clean("ignore_artifacts", {
    new_session  = true,
    session_name = "verify"
})

local verify_scope = scope("verify", {
    include      = { all_files },
    exclude      = { gitignored },
    capabilities = {
        "filesystem.read",
        "filesystem.glob",
        "process.execute"
    }
})

use({
    agent = dev,
    scope = verify_scope
})

task("verify", {
    step("run-tests", {
        execute("go test ./... 2>&1"),
        execute("go vet ./... 2>&1"),
        export()
    }),

    step("check-results", {
        depends_on("run-tests"),
        artifacts("fix-plan.md"),
        read(artifacts),
        reason(
            "Did all previously failing tests pass? " ..
            "Were any new failures introduced? " ..
            "Did the implementation follow the plan?"
        ),
        write(artifacts),
        artifacts("verification.md"),
        export()
    }),

    step("final-review", {
        depends_on("check-results"),
        artifacts("verification.md"),
        review(),
        guard("low")
    })
})

finish()
clear()
