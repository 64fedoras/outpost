package main

// Config export: settings, saved and scheduled tasks and node labels as one
// readable JSON file, with no keys, tokens or passwords (every field ending in
// _enc is left out), so a setup can be diffed, kept in git or copied to a
// second cluster. Importing never touches the secrets already on this
// controller and does not run anything.

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"
)

const configExportFormat = 1

type nodeConfig struct {
	Name   string   `json:"name"`
	Labels []string `json:"labels,omitempty"`
}

type configExport struct {
	Format     int                   `json:"format"`
	Outpost    string                `json:"outpost"`
	Exported   time.Time             `json:"exported"`
	Settings   map[string]any        `json:"settings"`
	Templates  []*Template           `json:"templates"`
	NodeLabels map[string]nodeConfig `json:"node_labels"` // by node id
}

// stripSecrets removes every key ending in _enc, at any depth.
func stripSecrets(v any) any {
	switch x := v.(type) {
	case map[string]any:
		for k, val := range x {
			if strings.HasSuffix(k, "_enc") {
				delete(x, k)
				continue
			}
			x[k] = stripSecrets(val)
		}
	case []any:
		for i := range x {
			x[i] = stripSecrets(x[i])
		}
	}
	return v
}

func (c *Controller) buildConfigExport() (*configExport, error) {
	c.mu.Lock()
	sb, err := json.Marshal(c.settings)
	tpl := append([]*Template{}, c.templates...)
	labels := map[string]nodeConfig{}
	for id, n := range c.nodes {
		if len(n.Labels) > 0 {
			labels[id] = nodeConfig{Name: n.Name, Labels: n.Labels}
		}
	}
	c.mu.Unlock()
	if err != nil {
		return nil, err
	}
	var settings map[string]any
	if err := json.Unmarshal(sb, &settings); err != nil {
		return nil, err
	}
	stripSecrets(settings)
	return &configExport{Format: configExportFormat, Outpost: Version, Exported: time.Now().UTC(), Settings: settings, Templates: tpl, NodeLabels: labels}, nil
}

func (c *Controller) handleConfigExport(w http.ResponseWriter, r *http.Request) {
	e, err := c.buildConfigExport()
	if err != nil {
		writeJSON(w, 500, map[string]string{"error": err.Error()})
		return
	}
	b, _ := json.MarshalIndent(e, "", "  ")
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="outpost-config-%s.json"`, time.Now().Format("2006-01-02")))
	w.Write(b)
}

// applyConfigImport merges an export into this controller: settings fields in
// the file replace the same fields here (secrets stay), templates are added or
// replaced by id, labels are applied to nodes with the same id.
func (c *Controller) applyConfigImport(e *configExport) (settings, templates, labels int, err error) {
	if e.Format != configExportFormat {
		return 0, 0, 0, fmt.Errorf("unsupported config format %d", e.Format)
	}
	stripSecrets(e.Settings) // a hand-edited file cannot plant keys either
	b, err := json.Marshal(e.Settings)
	if err != nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	next := c.settings
	if err = json.Unmarshal(b, &next); err != nil {
		return 0, 0, 0, fmt.Errorf("settings: %w", err)
	}
	c.settings = next
	settings = len(e.Settings)
	for _, t := range e.Templates {
		if t == nil || t.ID == "" {
			continue
		}
		if t.Schedule != nil {
			if t.Schedule.check() != nil {
				t.Schedule, t.NextRun = nil, nil
			} else {
				n := t.Schedule.next(time.Now())
				t.NextRun = &n
			}
		}
		if old := c.templateLocked(t.ID); old != nil {
			*old = *t
		} else {
			c.templates = append(c.templates, t)
		}
		templates++
	}
	for id, nc := range e.NodeLabels {
		if n := c.nodes[id]; n != nil {
			n.Labels = normLabels(nc.Labels)
			labels++
		}
	}
	if err = c.saveSettingsLocked(); err != nil {
		return
	}
	c.saveTemplatesLocked()
	c.saveNodesLocked()
	return
}

func (c *Controller) handleConfigImport(w http.ResponseWriter, r *http.Request) {
	var e configExport
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<20)).Decode(&e); err != nil {
		writeJSON(w, 400, map[string]string{"error": "not an Outpost config file: " + err.Error()})
		return
	}
	s, t, l, err := c.applyConfigImport(&e)
	if err != nil {
		writeJSON(w, 400, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, 200, map[string]int{"settings": s, "templates": t, "labels": l})
}
