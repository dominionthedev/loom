-- fix.lua
-- Loom workflow: analyse failures, plan a fix, implement, verify.
-- Demonstrates: table DSL syntax, depends_on order, artifact ref/create,
-- checkpoint with worktree, clean() for isolated verify, guard, review.

local model = "gemma4:32b"

local dev_agent = agent("dev", {
    model  = model,
    system = "You are an experienced developer. Precise. Minimal changes only.",
    think  = "medium"
})

local planner_agent = agent("planner", {
    model  = model,
    system = "You produce structured, actionable implementation plans.",
    think  = "high"
})

local impl_scope = scope("implementation", {
    include = {
        "**/*.go",
        "**/*_test.go",
        "go.mod"
    },
    exclude = {
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

local dev_rules = rule("dev-rules", {
    "do not modify go.sum or go.mod",
    "do not add new dependencies",
    "minimal changes only",
    "preserve existing code style",
})

memory({ persist = true, location = "local" })

use({
    agent    = dev_agent,
    scope    = impl_scope,
    policies = { no_git, no_mod },
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
        think("medium"),
        reason("Analyse the test output and source. Identify root cause of each failure."),
        export()
    })
})

-- ── Task 2: Plan ───────────────────────────────────────────────────────────

use({
    agent    = planner_agent,
    scope    = impl_scope,
    policies = { no_git, no_mod },
    rules    = { dev_rules }
})

task("plan", {
    step("create-plan", {
        depends_on("analyse-failures"),
        -- plan() always runs at high thinking — no think() needed
        plan("Create a precise implementation plan. List each change: file, function, what and why."),
        write(artifacts),
        artifacts("fix-plan.md"),
        export()
    }),

    step("review-plan", {
        depends_on("create-plan"),
        artifacts("fix-plan.md"), -- reference: load into context
        reason("Review the plan. Is it complete, precise, and safe?"),
        guard("important")
    })
})

-- Checkpoint: snapshot + create worktree before touching the project.
checkpoint("before-implementation", {
    type   = worktree,
    review = "fix-plan.md"
})

-- ── Task 3: Implement ──────────────────────────────────────────────────────

use({
    agent    = dev_agent,
    scope    = impl_scope,
    policies = { no_git, no_mod },
    rules    = { dev_rules }
})

task("implement", {
    step("apply-fix", {
        depends_on("review-plan"),
        artifacts("fix-plan.md"), -- reference: load plan into context
        reason("Implement the fixes from the plan. Follow it precisely."),
        all_capabilities(),
        guard("important"),
        export()
    }),

    step("run-tests-after", {
        depends_on("apply-fix"),
        execute("go test ./... 2>&1"),
        execute("go vet ./... 2>&1"),
        export()
    }),

    step("build-check", {
        depends_on("run-tests-after"),
        execute("go build ./... 2>&1"),
        export()
    })
})

-- ── Task 4: Verify (fresh context — agent can't hide problems) ─────────────

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
        "process.execute",
        "model.think"
    }
})

use({
    agent = dev_agent,
    scope = verify_scope
})

task("verify", {
    step("test", {
        execute("go test ./... 2>&1"),
        execute("go vet ./... 2>&1"),
        export()
    }),

    step("analyse-results", {
        depends_on("test"),
        artifacts("fix-plan.md"), -- reference: check against original plan
        think("medium"),
        reason("Did all failing tests pass? Were new failures introduced? Did implementation follow the plan?"),
        read("**/*.go"),
        write(artifacts),
        artifacts("verification.md"),
        export()
    }),

    step("final-review", {
        depends_on("analyse-results"),
        artifacts("verification.md"),
        review(),
        guard("low")
    })
})

finish()
clear()
