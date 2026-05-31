// Package dsl evaluates Loom workflow files.
// The file IS the workflow — no wrapper function.
// Syntax: task(name, { steps }) and step(name, { ops })
package dsl

import (
	"fmt"

	lua "github.com/yuin/gopher-lua"
	"github.com/dominionthedev/loom/internal/workflow"
)

// Eval evaluates a Loom workflow file and returns the parsed File.
func Eval(path string) (*workflow.File, error) {
	L := lua.NewState()
	defer L.Close()

	f := newFile()
	register(L, f)

	if err := L.DoFile(path); err != nil {
		return nil, fmt.Errorf("dsl: %w", err)
	}
	return f, nil
}

// EvalString evaluates Loom workflow source.
func EvalString(src string) (*workflow.File, error) {
	L := lua.NewState()
	defer L.Close()

	f := newFile()
	register(L, f)

	if err := L.DoString(src); err != nil {
		return nil, fmt.Errorf("dsl: %w", err)
	}
	return f, nil
}

func newFile() *workflow.File {
	return &workflow.File{
		Agents:     make(map[string]*workflow.AgentDef),
		Scopes:     make(map[string]*workflow.Scope),
		Policies:   make(map[string]*workflow.Policy),
		Rules:      make(map[string]*workflow.Rule),
		Workspaces: make(map[string]*workflow.WorkspaceDef),
	}
}

func register(L *lua.LState, f *workflow.File) {
	// Built-in constants.
	// "artifacts" is NOT set here — registered once as a function in
	// registerStepPrimitives to avoid being overwritten and causing
	// write(artifacts) to receive an LFunction instead of a sentinel value.
	L.SetGlobal("gitignored", lua.LString("__gitignored__"))
	L.SetGlobal("all_files", lua.LString("__all_files__"))
	L.SetGlobal("worktree", lua.LString("__worktree__"))

	registerAgent(L, f)
	registerScope(L, f)
	registerPolicy(L, f)
	registerRule(L, f)
	registerWorkspace(L, f)
	registerScript(L, f)
	registerMemory(L, f)
	registerUse(L, f)
	registerTask(L, f)
	registerCheckpoint(L, f)
	registerFinish(L, f)
	registerClear(L, f)
	registerClean(L, f)
}

// ── agent() ───────────────────────────────────────────────────────────────

func registerAgent(L *lua.LState, f *workflow.File) {
	L.SetGlobal("agent", L.NewFunction(func(L *lua.LState) int {
		name := L.CheckString(1)
		tbl := L.CheckTable(2)

		def := &workflow.AgentDef{
			Name:       name,
			Model:      strField(tbl, "model"),
			System:     strField(tbl, "system"),
			ThinkLevel: strField(tbl, "think"),
		}
		f.Agents[name] = def

		ref := L.NewTable()
		ref.RawSetString("_agent_name", lua.LString(name))
		L.Push(ref)
		return 1
	}))
}

// ── scope() ───────────────────────────────────────────────────────────────

func registerScope(L *lua.LState, f *workflow.File) {
	L.SetGlobal("scope", L.NewFunction(func(L *lua.LState) int {
		name := L.CheckString(1)
		tbl := L.CheckTable(2)

		s := &workflow.Scope{
			Name:         name,
			Include:      stringList(tbl.RawGetString("include")),
			Exclude:      stringList(tbl.RawGetString("exclude")),
			Capabilities: stringList(tbl.RawGetString("capabilities")),
		}
		f.Scopes[name] = s

		ref := L.NewTable()
		ref.RawSetString("_scope_name", lua.LString(name))
		L.Push(ref)
		return 1
	}))
}

// ── policy() ──────────────────────────────────────────────────────────────

func registerPolicy(L *lua.LState, f *workflow.File) {
	L.SetGlobal("policy", L.NewFunction(func(L *lua.LState) int {
		name := L.CheckString(1)
		tbl := L.CheckTable(2)

		var kind workflow.PolicyKind
		switch strField(tbl, "kind") {
		case "deny":
			kind = workflow.PolicyDeny
		case "allow":
			kind = workflow.PolicyAllow
		case "limit":
			kind = workflow.PolicyLimit
		}

		p := &workflow.Policy{
			Name:   name,
			Kind:   kind,
			Target: strField(tbl, "target"),
			Match:  stringList(tbl.RawGetString("match")),
		}
		f.Policies[name] = p

		ref := L.NewTable()
		ref.RawSetString("_policy_name", lua.LString(name))
		L.Push(ref)
		return 1
	}))
}

// ── rule() ────────────────────────────────────────────────────────────────

func registerRule(L *lua.LState, f *workflow.File) {
	L.SetGlobal("rule", L.NewFunction(func(L *lua.LState) int {
		name := L.CheckString(1)
		tbl := L.CheckTable(2)

		r := &workflow.Rule{
			Name:        name,
			Constraints: stringList(lua.LValue(tbl)),
		}
		f.Rules[name] = r

		ref := L.NewTable()
		ref.RawSetString("_rule_name", lua.LString(name))
		L.Push(ref)
		return 1
	}))
}

// ── workspace() ───────────────────────────────────────────────────────────

func registerWorkspace(L *lua.LState, f *workflow.File) {
	L.SetGlobal("workspace", L.NewFunction(func(L *lua.LState) int {
		name := L.CheckString(1)
		tbl := L.CheckTable(2)

		ws := &workflow.WorkspaceDef{
			Name:    name,
			Dir:     strFieldDefault(tbl, "dir", "./"),
			Shell:   strFieldDefault(tbl, "shell", "/bin/sh"),
			Source:  stringList(tbl.RawGetString("source")),
			EnvFile: strField(tbl, "env_file"),
		}
		f.Workspaces[name] = ws

		ref := L.NewTable()
		ref.RawSetString("_workspace_name", lua.LString(name))
		L.Push(ref)
		return 1
	}))
}

// ── script() ──────────────────────────────────────────────────────────────

func registerScript(L *lua.LState, f *workflow.File) {
	L.SetGlobal("script", L.NewFunction(func(L *lua.LState) int {
		name := L.CheckString(1)
		tbl := L.CheckTable(2)

		var trigger workflow.ScriptTrigger
		switch strField(tbl, "trigger") {
		case "on_failure":
			trigger = workflow.TriggerOnFailure
		case "after_task":
			trigger = workflow.TriggerAfterTask
		case "on_checkpoint":
			trigger = workflow.TriggerOnCheckpoint
		case "before_step":
			trigger = workflow.TriggerBeforeStep
		case "after_step":
			trigger = workflow.TriggerAfterStep
		}

		var guard workflow.GuardLevel
		switch strField(tbl, "require") {
		case "important":
			guard = workflow.GuardImportant
		case "critical":
			guard = workflow.GuardCritical
		default:
			guard = workflow.GuardLow
		}

		f.Scripts = append(f.Scripts, &workflow.ScriptDef{
			Name:    name,
			Source:  strField(tbl, "source"),
			Trigger: trigger,
			Require: guard,
		})
		return 0
	}))
}

// ── memory() ──────────────────────────────────────────────────────────────

func registerMemory(L *lua.LState, f *workflow.File) {
	L.SetGlobal("memory", L.NewFunction(func(L *lua.LState) int {
		if L.GetTop() == 0 {
			f.Memory = &workflow.MemoryConfig{Persist: true, Location: "local"}
			return 0
		}
		tbl := L.CheckTable(1)
		f.Memory = &workflow.MemoryConfig{
			Persist:  boolFieldDefault(tbl, "persist", true),
			Location: strFieldDefault(tbl, "location", "local"),
			Compress: boolField(tbl, "compress"),
		}
		return 0
	}))
}

// ── use() ─────────────────────────────────────────────────────────────────

func registerUse(L *lua.LState, f *workflow.File) {
	L.SetGlobal("use", L.NewFunction(func(L *lua.LState) int {
		cfg := &workflow.UseConfig{}

		start := 1
		if L.GetTop() >= 1 {
			if s, ok := L.Get(1).(lua.LString); ok && string(s) == "defaults" {
				start = 2
			}
		}

		if L.GetTop() >= start {
			if tbl, ok := L.Get(start).(*lua.LTable); ok {
				cfg = resolveUseConfig(tbl, f)
			}
		}

		// use() clears previous use() — does NOT clear context/state/checkpoints.
		f.Use = cfg
		return 0
	}))
}

func resolveUseConfig(tbl *lua.LTable, f *workflow.File) *workflow.UseConfig {
	cfg := &workflow.UseConfig{}

	if av := tbl.RawGetString("agent"); av != lua.LNil {
		if at, ok := av.(*lua.LTable); ok {
			if n := at.RawGetString("_agent_name"); n != lua.LNil {
				cfg.Agent = f.Agents[string(n.(lua.LString))]
			}
		}
	}

	if sv := tbl.RawGetString("scope"); sv != lua.LNil {
		if st, ok := sv.(*lua.LTable); ok {
			if n := st.RawGetString("_scope_name"); n != lua.LNil {
				cfg.Scope = f.Scopes[string(n.(lua.LString))]
			}
		}
	}

	if pv := tbl.RawGetString("policies"); pv != lua.LNil {
		if pt, ok := pv.(*lua.LTable); ok {
			pt.ForEach(func(_, v lua.LValue) {
				if ref, ok := v.(*lua.LTable); ok {
					if n := ref.RawGetString("_policy_name"); n != lua.LNil {
						if p, ok := f.Policies[string(n.(lua.LString))]; ok {
							cfg.Policies = append(cfg.Policies, p)
						}
					}
				}
			})
		}
	}

	if rv := tbl.RawGetString("rules"); rv != lua.LNil {
		if rt, ok := rv.(*lua.LTable); ok {
			rt.ForEach(func(_, v lua.LValue) {
				if ref, ok := v.(*lua.LTable); ok {
					if n := ref.RawGetString("_rule_name"); n != lua.LNil {
						if r, ok := f.Rules[string(n.(lua.LString))]; ok {
							cfg.Rules = append(cfg.Rules, r)
						}
					}
				}
			})
		}
	}

	return cfg
}

// ── task(name, { step(...), step(...), ... }) ──────────────────────────────
//
// task and step now use TABLE syntax — not variadic.
// task("name", { step("a", {...}), step("b", {...}) })
// step("name", { depends_on("x"), reason("..."), execute(), export() })

func registerTask(L *lua.LState, f *workflow.File) {
	L.SetGlobal("task", L.NewFunction(func(L *lua.LState) int {
		name := L.CheckString(1)
		stepsTbl := L.CheckTable(2)

		task := &workflow.Task{
			Name: name,
			Use:  f.Use, // snapshot current use() at definition time
		}

		stepsTbl.ForEach(func(_, v lua.LValue) {
			if tbl, ok := v.(*lua.LTable); ok {
				if s := tableToStep(tbl); s != nil {
					task.Steps = append(task.Steps, s)
				}
			}
		})

		f.Sequence = append(f.Sequence, workflow.SequenceItem{
			Kind: workflow.SeqTask,
			Task: task,
		})
		return 0
	}))

	// step("name", { op(), op(), ... })
	L.SetGlobal("step", L.NewFunction(func(L *lua.LState) int {
		name := L.CheckString(1)
		opsTbl := L.CheckTable(2)

		tbl := L.NewTable()
		tbl.RawSetString("_step_name", lua.LString(name))
		tbl.RawSetString("_ops", opsTbl)

		L.Push(tbl)
		return 1
	}))

	registerStepPrimitives(L)
}

// ── Step primitives ───────────────────────────────────────────────────────

func registerStepPrimitives(L *lua.LState) {
	op := func(name string, populate func(*lua.LState, *lua.LTable)) {
		L.SetGlobal(name, L.NewFunction(func(L *lua.LState) int {
			tbl := L.NewTable()
			tbl.RawSetString("_op", lua.LString(name))
			populate(L, tbl)
			L.Push(tbl)
			return 1
		}))
	}

	// think("level")
	op("think", func(L *lua.LState, t *lua.LTable) {
		if L.GetTop() >= 1 {
			t.RawSetString("level", L.Get(1))
		}
	})

	// reason("prompt")
	op("reason", func(L *lua.LState, t *lua.LTable) {
		if L.GetTop() >= 1 {
			t.RawSetString("prompt", L.Get(1))
		}
	})

	// plan("prompt") — always high thinking, guard escalates
	op("plan", func(L *lua.LState, t *lua.LTable) {
		if L.GetTop() >= 1 {
			t.RawSetString("prompt", L.Get(1))
		}
	})

	// read("pattern" | artifacts)
	// If artifacts (an LFunction) is passed, treat as __artifacts__ sentinel.
	op("read", func(L *lua.LState, t *lua.LTable) {
		if L.GetTop() >= 1 {
			v := L.Get(1)
			if _, isFunc := v.(*lua.LFunction); isFunc {
				t.RawSetString("target", lua.LString("__artifacts__"))
			} else {
				t.RawSetString("target", v)
			}
		}
	})

	// write("pattern" | artifacts)
	// If artifacts (an LFunction) is passed, treat as __artifacts__ sentinel.
	op("write", func(L *lua.LState, t *lua.LTable) {
		if L.GetTop() >= 1 {
			v := L.Get(1)
			if _, isFunc := v.(*lua.LFunction); isFunc {
				t.RawSetString("target", lua.LString("__artifacts__"))
			} else {
				t.RawSetString("target", v)
			}
		}
	})

	// execute("cmd1", "cmd2", ...) or execute() = open
	op("execute", func(L *lua.LState, t *lua.LTable) {
		args := L.NewTable()
		for i := 1; i <= L.GetTop(); i++ {
			args.Append(L.Get(i))
		}
		t.RawSetString("args", args)
	})

	// background("cmd")
	op("background", func(L *lua.LState, t *lua.LTable) {
		if L.GetTop() >= 1 {
			t.RawSetString("cmd", L.Get(1))
		}
	})

	// watch("pattern")
	op("watch", func(L *lua.LState, t *lua.LTable) {
		if L.GetTop() >= 1 {
			t.RawSetString("pattern", L.Get(1))
		}
	})

	// glob("pattern")
	op("glob", func(L *lua.LState, t *lua.LTable) {
		if L.GetTop() >= 1 {
			t.RawSetString("pattern", L.Get(1))
		}
	})

	// edit("file")
	op("edit", func(L *lua.LState, t *lua.LTable) {
		if L.GetTop() >= 1 {
			t.RawSetString("file", L.Get(1))
		}
	})

	// all_capabilities()
	op("all_capabilities", func(L *lua.LState, t *lua.LTable) {
		t.RawSetString("all", lua.LTrue)
	})

	// capability("name") — user-defined
	op("capability", func(L *lua.LState, t *lua.LTable) {
		if L.GetTop() >= 1 {
			t.RawSetString("name", L.Get(1))
		}
	})

	// depends_on("step1", "step2", ...)
	// Comes FIRST in step — imports context + enforces DAG.
	op("depends_on", func(L *lua.LState, t *lua.LTable) {
		deps := L.NewTable()
		for i := 1; i <= L.GetTop(); i++ {
			deps.Append(L.Get(i))
		}
		t.RawSetString("deps", deps)
	})

	// export() — make step context available downstream via depends_on.
	// Requires memory() to be initialized for cross-task sharing.
	op("export", func(L *lua.LState, t *lua.LTable) {
		t.RawSetString("export", lua.LTrue)
	})

	// artifacts("name") — reference (before reasoning) or create (after write/plan).
	// Position in the step ops list determines ref vs create.
	L.SetGlobal("artifacts", L.NewFunction(func(L *lua.LState) int {
		if L.GetTop() >= 1 {
			if s, ok := L.Get(1).(lua.LString); ok {
				tbl := L.NewTable()
				tbl.RawSetString("_op", lua.LString("artifacts"))
				tbl.RawSetString("name", s)
				L.Push(tbl)
				return 1
			}
		}
		// No args — return the artifacts directory constant.
		L.Push(lua.LString("__artifacts__"))
		return 1
	}))

	// guard("level")
	op("guard", func(L *lua.LState, t *lua.LTable) {
		if L.GetTop() >= 1 {
			t.RawSetString("level", L.Get(1))
		}
	})

	// review() or review("agent-name")
	op("review", func(L *lua.LState, t *lua.LTable) {
		if L.GetTop() >= 1 {
			t.RawSetString("agent", L.Get(1))
		}
	})
}

// ── checkpoint() ──────────────────────────────────────────────────────────

func registerCheckpoint(L *lua.LState, f *workflow.File) {
	L.SetGlobal("checkpoint", L.NewFunction(func(L *lua.LState) int {
		label := L.CheckString(1)
		cp := &workflow.CheckpointDef{Label: label, Type: workflow.CheckpointState}

		if L.GetTop() >= 2 {
			if tbl, ok := L.Get(2).(*lua.LTable); ok {
				if tv := tbl.RawGetString("type"); tv != lua.LNil {
					if s, ok := tv.(lua.LString); ok && string(s) == "__worktree__" {
						cp.Type = workflow.CheckpointWorktree
					}
				}
				cp.ReviewFile = strField(tbl, "review")
			}
		}

		f.Sequence = append(f.Sequence, workflow.SequenceItem{
			Kind:       workflow.SeqCheckpoint,
			Checkpoint: cp,
		})
		return 0
	}))
}

func registerFinish(L *lua.LState, f *workflow.File) {
	L.SetGlobal("finish", L.NewFunction(func(L *lua.LState) int {
		f.Sequence = append(f.Sequence, workflow.SequenceItem{Kind: workflow.SeqFinish})
		return 0
	}))
}

func registerClear(L *lua.LState, f *workflow.File) {
	L.SetGlobal("clear", L.NewFunction(func(L *lua.LState) int {
		f.Sequence = append(f.Sequence, workflow.SequenceItem{Kind: workflow.SeqClear})
		return 0
	}))
}

func registerClean(L *lua.LState, f *workflow.File) {
	L.SetGlobal("clean", L.NewFunction(func(L *lua.LState) int {
		cfg := &workflow.CleanConfig{}
		if L.GetTop() >= 1 {
			if s, ok := L.Get(1).(lua.LString); ok {
				cfg.Ignore = append(cfg.Ignore, string(s))
			}
		}
		if L.GetTop() >= 2 {
			if tbl, ok := L.Get(2).(*lua.LTable); ok {
				cfg.NewSession = boolField(tbl, "new_session")
				cfg.SessionName = strField(tbl, "session_name")
			}
		}
		f.Sequence = append(f.Sequence, workflow.SequenceItem{
			Kind:  workflow.SeqClean,
			Clean: cfg,
		})
		return 0
	}))
}

// ── tableToStep ───────────────────────────────────────────────────────────

// tableToStep converts a step(name, {ops}) result to a *workflow.Step.
// Op order in the table determines artifact ref vs create:
// artifacts() BEFORE reason()/plan() = reference (load into context).
// artifacts() AFTER write()/plan() = create.
func tableToStep(tbl *lua.LTable) *workflow.Step {
	nameVal := tbl.RawGetString("_step_name")
	if nameVal == lua.LNil {
		return nil
	}

	step := &workflow.Step{
		Name:      string(nameVal.(lua.LString)),
		OnFailure: workflow.OnFailureStop,
	}

	opsVal := tbl.RawGetString("_ops")
	if opsVal == lua.LNil {
		return step
	}
	ops, ok := opsVal.(*lua.LTable)
	if !ok {
		return step
	}

	// Track whether we've seen a write/plan — determines artifact create vs ref.
	seenWrite := false

	ops.ForEach(func(_, v lua.LValue) {
		opTbl, ok := v.(*lua.LTable)
		if !ok {
			return
		}
		opName := ""
		if ov := opTbl.RawGetString("_op"); ov != lua.LNil {
			opName = string(ov.(lua.LString))
		}

		switch opName {
		case "think":
			if lv := opTbl.RawGetString("level"); lv != lua.LNil {
				step.ThinkLevel = string(lv.(lua.LString))
			}

		case "reason":
			step.Kind = workflow.StepReason
			if pv := opTbl.RawGetString("prompt"); pv != lua.LNil {
				step.Prompt = string(pv.(lua.LString))
			}

		case "plan":
			// plan() always uses high thinking — think() cannot override it.
			step.Kind = workflow.StepPlan
			if pv := opTbl.RawGetString("prompt"); pv != lua.LNil {
				step.Prompt = string(pv.(lua.LString))
			}
			seenWrite = true // plan produces an artifact

		case "read":
			target := ""
			if tv := opTbl.RawGetString("target"); tv != lua.LNil {
				target = string(tv.(lua.LString))
			}
			step.CapCalls = append(step.CapCalls, workflow.CapCall{
				Name: "filesystem.read",
				Args: nonEmpty(target),
			})

		case "write":
			target := ""
			if tv := opTbl.RawGetString("target"); tv != lua.LNil {
				target = string(tv.(lua.LString))
			}
			step.CapCalls = append(step.CapCalls, workflow.CapCall{
				Name: "filesystem.write",
				Args: nonEmpty(target),
			})
			seenWrite = true

		case "execute":
			call := workflow.CapCall{Name: "process.execute"}
			if av := opTbl.RawGetString("args"); av != lua.LNil {
				if at, ok := av.(*lua.LTable); ok {
					call.Args = tableToStringSlice(at)
				}
			}
			step.CapCalls = append(step.CapCalls, call)

		case "background":
			call := workflow.CapCall{Name: "process.background"}
			if cv := opTbl.RawGetString("cmd"); cv != lua.LNil {
				call.Args = []string{string(cv.(lua.LString))}
			}
			step.CapCalls = append(step.CapCalls, call)

		case "watch":
			call := workflow.CapCall{Name: "process.watch"}
			if pv := opTbl.RawGetString("pattern"); pv != lua.LNil {
				call.Args = []string{string(pv.(lua.LString))}
			}
			step.CapCalls = append(step.CapCalls, call)

		case "glob":
			call := workflow.CapCall{Name: "filesystem.glob"}
			if pv := opTbl.RawGetString("pattern"); pv != lua.LNil {
				call.Args = []string{string(pv.(lua.LString))}
			}
			step.CapCalls = append(step.CapCalls, call)

		case "edit":
			call := workflow.CapCall{Name: "filesystem.edit"}
			if fv := opTbl.RawGetString("file"); fv != lua.LNil {
				call.Args = []string{string(fv.(lua.LString))}
			}
			step.CapCalls = append(step.CapCalls, call)

		case "all_capabilities":
			step.CapCalls = append(step.CapCalls, workflow.CapCall{All: true})

		case "capability":
			call := workflow.CapCall{Custom: true}
			if nv := opTbl.RawGetString("name"); nv != lua.LNil {
				call.Name = string(nv.(lua.LString))
			}
			step.CapCalls = append(step.CapCalls, call)

		case "depends_on":
			if dv := opTbl.RawGetString("deps"); dv != lua.LNil {
				if dt, ok := dv.(*lua.LTable); ok {
					step.DependsOn = tableToStringSlice(dt)
				}
			}

		case "export":
			step.Export = true

		case "artifacts":
			name := ""
			if nv := opTbl.RawGetString("name"); nv != lua.LNil {
				name = string(nv.(lua.LString))
			}
			// Position determines ref vs create:
			// Before write/plan = reference. After = create.
			step.ArtifactRefs = append(step.ArtifactRefs, workflow.ArtifactRef{
				Name:   name,
				Create: seenWrite,
			})

		case "guard":
			if lv := opTbl.RawGetString("level"); lv != lua.LNil {
				switch string(lv.(lua.LString)) {
				case "important":
					step.Guard = workflow.GuardImportant
				case "critical":
					step.Guard = workflow.GuardCritical
				default:
					step.Guard = workflow.GuardLow
				}
			}

		case "review":
			step.Review = true
			if av := opTbl.RawGetString("agent"); av != lua.LNil {
				step.ReviewAgent = string(av.(lua.LString))
			}
		}
	})

	return step
}

// ── Helpers ────────────────────────────────────────────────────────────────

func strField(tbl *lua.LTable, key string) string {
	if s, ok := tbl.RawGetString(key).(lua.LString); ok {
		return string(s)
	}
	return ""
}

func strFieldDefault(tbl *lua.LTable, key, def string) string {
	if v := strField(tbl, key); v != "" {
		return v
	}
	return def
}

func boolField(tbl *lua.LTable, key string) bool {
	if b, ok := tbl.RawGetString(key).(lua.LBool); ok {
		return bool(b)
	}
	return false
}

func boolFieldDefault(tbl *lua.LTable, key string, def bool) bool {
	v := tbl.RawGetString(key)
	if v == lua.LNil {
		return def
	}
	if b, ok := v.(lua.LBool); ok {
		return bool(b)
	}
	return def
}

func stringList(v lua.LValue) []string {
	tbl, ok := v.(*lua.LTable)
	if !ok {
		return nil
	}
	var out []string
	tbl.ForEach(func(_, val lua.LValue) {
		if s, ok := val.(lua.LString); ok {
			out = append(out, string(s))
		}
	})
	return out
}

func tableToStringSlice(tbl *lua.LTable) []string {
	var out []string
	tbl.ForEach(func(_, v lua.LValue) {
		if s, ok := v.(lua.LString); ok {
			out = append(out, string(s))
		}
	})
	return out
}

func nonEmpty(s string) []string {
	if s == "" {
		return nil
	}
	return []string{s}
}
