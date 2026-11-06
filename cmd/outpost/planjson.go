package main

// Constrained JSON for local planners, only as a retry (idea 8, owner's
// choice): when a local model's plan is not valid JSON, the next attempt asks
// llama-server to hold the answer to the plan's shape with a JSON schema. The
// first attempt stays free, so models that work today are not changed, and a
// llama.cpp build without schema support simply answers without it.

import (
	"encoding/json"
	"errors"
)

// parseError marks an answer that was not valid JSON.
type parseError struct{ err error }

func (e *parseError) Error() string { return e.err.Error() }
func (e *parseError) Unwrap() error { return e.err }

func parseFailed(err error) bool {
	var pe *parseError
	return errors.As(err, &pe)
}

func (t *Task) cloudPlanner() bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	return isCloudPlanner(t.Planner)
}

// planSchema accepts the three answers the planner may give: a plan,
// questions, or a request for files from the project index.
var planSchema = json.RawMessage(`{
  "anyOf": [
    {"type": "object", "required": ["title", "summary", "subtasks"], "properties": {
      "title": {"type": "string"}, "summary": {"type": "string"}, "language": {"type": "string"},
      "setup_command": {"type": "string"}, "test_command": {"type": "string"}, "build_command": {"type": "string"},
      "artifacts": {"type": "array", "items": {"type": "string"}},
      "preview_page": {"type": "string"}, "preview_command": {"type": "string"}, "preview_url": {"type": "string"},
      "note": {"type": "string"},
      "delete": {"type": "array", "items": {"type": "string"}},
      "rename": {"type": "array", "items": {"type": "object", "required": ["from", "to"], "properties": {"from": {"type": "string"}, "to": {"type": "string"}}}},
      "subtasks": {"type": "array", "items": {"type": "object", "required": ["id", "title", "files", "instructions"], "properties": {
        "id": {"type": "string"}, "title": {"type": "string"},
        "files": {"type": "array", "items": {"type": "string"}},
        "depends_on": {"type": "array", "items": {"type": "string"}},
        "difficulty": {"type": "string", "enum": ["easy", "normal", "hard"]},
        "instructions": {"type": "string"}}}}}},
    {"type": "object", "required": ["questions"], "properties": {
      "questions": {"type": "array", "items": {"type": "object", "required": ["id", "question", "options"], "properties": {
        "id": {"type": "string"}, "question": {"type": "string"},
        "options": {"type": "array", "items": {"type": "string"}}, "multi": {"type": "boolean"}}}},
      "note": {"type": "string"}}},
    {"type": "object", "required": ["need_files"], "properties": {"need_files": {"type": "array", "items": {"type": "string"}}}}
  ]
}`)
