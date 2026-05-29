-- review.lua
-- Demonstrates: parallel tasks via depends_on, no-write policy enforcement

local no_write = policy("no-write", {
    kind   = "deny",
    target = "filesystem.write",
    match  = {} -- no match = deny the whole capability
})

local reviewer = agent("reviewer", {
    model  = "gemma4:32b",
    system = "You are a thorough code reviewer. Be specific. Flag real issues only.",
    think  = "medium"
})

local review_scope = scope("review", {
    include      = { "**/*.go", "**/*.lua", "README.md" },
    exclude      = { "vendor/**", ".loom/**", gitignored },
    capabilities = { "filesystem.read", "filesystem.glob", "process.execute" }
})

memory({ persist = true, location = "local" })

use({
    agent    = reviewer,
    scope    = review_scope,
    policies = { no_write }
})

task("review", {

    step("read-code",
        glob("**/*.go"),
        read(),
        export()
    ),

    -- Three independent review tasks — same graph depth, run in parallel.
    step("security",
        depends_on("read-code"),
        reason("Review the code for security vulnerabilities and unsafe patterns."),
        export()
    ),

    step("performance",
        depends_on("read-code"),
        reason("Review the code for performance issues and unnecessary allocations."),
        export()
    ),

    step("style",
        depends_on("read-code"),
        reason("Review the code for Go idioms, naming conventions, and documentation."),
        export()
    ),

    -- Synthesize after all three complete.
    step("synthesize",
        depends_on("security", "performance", "style"),
        reason("Synthesize all findings into a prioritized report: critical, major, minor."),
        write(artifacts), -- the policy won't block this, because you are writing to artifacts, which is needed
        artifacts("review-report.md")
    )

})

finish()
