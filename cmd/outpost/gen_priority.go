package main

// Priority between coding tasks and picture work on the same cluster.
//   - A node that is rendering a picture or clip is not handed coding calls
//     (the renderer and llama-server would fight over its memory).
//   - Settings → Pictures can also hold back all new picture work while any
//     coding task is working, so a long video render cannot starve a task.
// Pictures already rendering are never interrupted.

import (
	"strconv"
	"strings"
)

// nodeMakingPicture says whether the node is rendering now. c.mu may be held;
// the picture lock is always taken after it (as dispatchGens does).
func (c *Controller) nodeMakingPicture(id string) bool {
	if c.gens == nil {
		return false
	}
	c.gens.mu.Lock()
	defer c.gens.mu.Unlock()
	return c.gens.busy[id]
}

// anyTaskWorking says whether a coding task is planning, writing, testing or
// fixing. It must not be called with c.mu held: task locks are taken one by
// one after the list is copied.
func (c *Controller) anyTaskWorking() bool {
	c.mu.Lock()
	list := make([]*Task, 0, len(c.tasks))
	for _, t := range c.tasks {
		list = append(list, t)
	}
	c.mu.Unlock()
	for _, t := range list {
		t.mu.Lock()
		s := t.Status
		t.mu.Unlock()
		switch s {
		case "planning", "running", "testing", "building", "fixing":
			return true
		}
	}
	return false
}

// matches says whether a request's prompt, model, label, source or one of its
// seeds contains q (case-insensitive).
func (g *GenRec) matches(q string) bool {
	q = strings.ToLower(q)
	for _, f := range []string{g.Prompt, g.Negative, g.Model, g.Label, g.Source} {
		if strings.Contains(strings.ToLower(f), q) {
			return true
		}
	}
	for _, it := range g.Items {
		if strconv.FormatInt(it.Seed, 10) == q {
			return true
		}
	}
	return false
}
