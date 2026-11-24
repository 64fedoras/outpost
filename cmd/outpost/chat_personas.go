package main

// Personas: named sets of chat instructions kept in Settings, so a new chat
// can start from "Code reviewer" or "Plain English" instead of retyping them.
// Picking one only fills the chat's instructions box; the chat keeps its own
// copy, so editing a persona later does not change old chats.

import (
	"strings"
)

const (
	maxPersonas    = 30
	maxPersonaName = 40
)

type ChatPersona struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	Text string `json:"text"`
}

func personasOrEmpty(p []ChatPersona) []ChatPersona {
	if p == nil {
		return []ChatPersona{}
	}
	return p
}

// cleanPersonas trims names and texts, drops empty or repeated names, gives new
// ones an id and keeps at most maxPersonas.
func cleanPersonas(in []ChatPersona) []ChatPersona {
	out := []ChatPersona{}
	names := map[string]bool{}
	ids := map[string]bool{}
	for _, p := range in {
		name := strings.Join(strings.Fields(p.Name), " ")
		if r := []rune(name); len(r) > maxPersonaName {
			name = string(r[:maxPersonaName])
		}
		text := clipChatInstructions(p.Text)
		if name == "" || text == "" || names[strings.ToLower(name)] {
			continue
		}
		names[strings.ToLower(name)] = true
		id := p.ID
		if id == "" || ids[id] || len(id) > 40 || strings.ContainsAny(id, " \"<>&") {
			id = newID("persona")
		}
		ids[id] = true
		out = append(out, ChatPersona{ID: id, Name: name, Text: text})
		if len(out) == maxPersonas {
			break
		}
	}
	return out
}
