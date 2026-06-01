-- review.lua
-- Parallel code review tasks.
-- Demonstrates: parallel steps via depends_on, no-write policy.
-- Run: loom run examples/review.lua

local no_write = policy("no-write", {
    kind   = "deny",
    target = "filesystem.write",
    match  = {}
})

local reviewer = agent("reviewer", {
    model  = "gemma3:4b",
    system = "You are a thorough code reviewer. Be specific. Flag real issues only.",
    think  = "low"
})

local review_scope = scope("review", {
    include      = { "**/*.go", "README.md" },
    exclude      = { "vendor/**", ".loom/**", gitignored },
    capabilities = { "filesystem.read", "filesystem.glob" }
})

memory({ persist = true, location = "local" })

use({
    agent    = reviewer,
    scope    = review_scope,
    policies = { no_write }
})

task("review", {
    -- Read source first.
    step("read-code", {
        glob("**/*.go"),
        read(),
        export()
    }),

    -- Three independent steps — same graph depth, run in parallel.
    step("security", {
        depends_on("read-code"),
        reason("Review the code for security vulnerabilities and unsafe patterns."),
        export()
    }),

    step("performance", {
        depends_on("read-code"),
        reason("Review the code for performance issues and unnecessary allocations."),
        export()
    }),

    step("style", {
        depends_on("read-code"),
        reason("Review the code for Go idioms, naming conventions, and documentation quality."),
        export()
    }),

    -- Synthesize after all three complete.
    step("synthesize", {
        depends_on("security", "performance", "style"),
        reason("Synthesize all findings into a prioritized report: critical, major, minor."),
        write(artifacts),
        artifacts("review-report.md")
    })
})

finish()
