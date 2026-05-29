// Package graph builds and validates execution graphs from workflow steps.
// Steps with no dependencies run at depth 0.
// Steps at the same depth are independent and run in parallel.
// Steps with depends_on wait for their dependencies before executing.
package graph

import (
	"fmt"

	"github.com/dominionthedev/loom/internal/workflow"
)

// Node wraps a step in the execution graph.
type Node struct {
	Step  *workflow.Step
	Deps  []*Node
	Depth int // topological depth — 0 means no dependencies
}

// Graph is the resolved execution graph for a task's steps.
type Graph struct {
	Nodes  []*Node
	byName map[string]*Node
	// Levels groups nodes by depth.
	// All nodes at the same level are independent — safe to run in parallel.
	Levels [][]*Node
}

// Build constructs and validates an execution graph.
// Returns an error if a depends_on references an unknown step, or if cycles exist.
func Build(steps []*workflow.Step) (*Graph, error) {
	g := &Graph{byName: make(map[string]*Node)}

	// First pass — create nodes.
	for _, s := range steps {
		n := &Node{Step: s}
		g.Nodes = append(g.Nodes, n)
		g.byName[s.Name] = n
	}

	// Second pass — resolve dependencies.
	for _, n := range g.Nodes {
		for _, depName := range n.Step.DependsOn {
			dep, ok := g.byName[depName]
			if !ok {
				return nil, fmt.Errorf("graph: step %q depends_on unknown step %q", n.Step.Name, depName)
			}
			n.Deps = append(n.Deps, dep)
		}
	}

	// Cycle detection.
	if err := detectCycles(g.Nodes); err != nil {
		return nil, err
	}

	// Compute depths.
	if err := computeDepths(g.Nodes); err != nil {
		return nil, err
	}

	// Group into levels.
	g.Levels = buildLevels(g.Nodes)

	return g, nil
}

func buildLevels(nodes []*Node) [][]*Node {
	max := 0
	for _, n := range nodes {
		if n.Depth > max {
			max = n.Depth
		}
	}
	levels := make([][]*Node, max+1)
	for _, n := range nodes {
		levels[n.Depth] = append(levels[n.Depth], n)
	}
	return levels
}

func computeDepths(nodes []*Node) error {
	computed := make(map[string]bool)

	var compute func(n *Node, visiting map[string]bool) error
	compute = func(n *Node, visiting map[string]bool) error {
		if computed[n.Step.Name] {
			return nil
		}
		if visiting[n.Step.Name] {
			return fmt.Errorf("graph: cycle at step %q", n.Step.Name)
		}
		visiting[n.Step.Name] = true

		maxDep := -1
		for _, dep := range n.Deps {
			if err := compute(dep, visiting); err != nil {
				return err
			}
			if dep.Depth > maxDep {
				maxDep = dep.Depth
			}
		}
		n.Depth = maxDep + 1
		computed[n.Step.Name] = true
		delete(visiting, n.Step.Name)
		return nil
	}

	for _, n := range nodes {
		if err := compute(n, make(map[string]bool)); err != nil {
			return err
		}
	}
	return nil
}

func detectCycles(nodes []*Node) error {
	visited := make(map[string]bool)
	inStack := make(map[string]bool)

	var dfs func(n *Node) error
	dfs = func(n *Node) error {
		if inStack[n.Step.Name] {
			return fmt.Errorf("graph: circular dependency at step %q", n.Step.Name)
		}
		if visited[n.Step.Name] {
			return nil
		}
		visited[n.Step.Name] = true
		inStack[n.Step.Name] = true
		for _, dep := range n.Deps {
			if err := dfs(dep); err != nil {
				return err
			}
		}
		inStack[n.Step.Name] = false
		return nil
	}

	for _, n := range nodes {
		if err := dfs(n); err != nil {
			return err
		}
	}
	return nil
}
