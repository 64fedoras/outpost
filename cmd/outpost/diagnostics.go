package main

// Diagnostics: one zip to attach to a bug report, and node logs on demand.
//
// The zip holds the controller's recent log, versions, node hardware and
// state, the event logs of recent tasks and Settings with every key, token,
// password and URL removed. Node logs are fetched from each online node over
// its existing connection (node_ask.go). Nothing is written to disk.

import (
	"archive/zip"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"regexp"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// ctlLog keeps the end of the controller's own log in memory (on Linux the
// log otherwise only goes to journald). Installed by runController.
var ctlLog = &ringLog{max: 1 << 20}

var ctlStarted = time.Now()

// handleNodeLogs returns the tail of one of a node's logs.
// GET /api/nodes/{id}/logs?source=node|tray|llama&lines=500
func (c *Controller) handleNodeLogs(w http.ResponseWriter, r *http.Request) {
	lines, _ := strconv.Atoi(r.URL.Query().Get("lines"))
	var out LogTail
	err := c.ask(r.PathValue("id"), "log.tail", LogTailArgs{Source: r.URL.Query().Get("source"), Lines: lines}, 10*time.Second, &out)
	if err != nil {
		writeJSON(w, 502, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, 200, out)
}

// secretKey matches settings fields that must never leave the controller.
var secretKey = regexp.MustCompile(`(?i)(enc|token|key|secret|pass(word)?|url|hash)$`)

// redact blanks every secret-looking field in a JSON value.
func redact(v any) any {
	switch t := v.(type) {
	case map[string]any:
		for k, x := range t {
			if secretKey.MatchString(k) {
				if s, ok := x.(string); ok && s == "" {
					continue
				}
				t[k] = "(removed)"
				continue
			}
			t[k] = redact(x)
		}
	case []any:
		for i := range t {
			t[i] = redact(t[i])
		}
	}
	return v
}

func redactedJSON(x any) []byte {
	b, _ := json.Marshal(x)
	var v any
	_ = json.Unmarshal(b, &v)
	out, _ := json.MarshalIndent(redact(v), "", "  ")
	return out
}

type diagOptions struct {
	NodeLogs bool `json:"node_logs"`
	TrayLogs bool `json:"tray_logs"`
	Prompts  bool `json:"prompts"`
}

// handleDiagnostics streams the zip. POST /api/diagnostics
func (c *Controller) handleDiagnostics(w http.ResponseWriter, r *http.Request) {
	opt := diagOptions{NodeLogs: true, TrayLogs: true}
	_ = json.NewDecoder(io.LimitReader(r.Body, 4096)).Decode(&opt)
	name := "outpost-diagnostics-" + time.Now().Format("2006-01-02-1504") + ".zip"
	w.Header().Set("Content-Type", "application/zip")
	w.Header().Set("Content-Disposition", `attachment; filename="`+name+`"`)
	if err := c.writeDiagnostics(w, opt); err != nil {
		log.Printf("diagnostics: %v", err)
	}
}

func (c *Controller) writeDiagnostics(w io.Writer, opt diagOptions) error {
	z := zip.NewWriter(w)
	add := func(name string, b []byte) {
		if f, err := z.CreateHeader(&zip.FileHeader{Name: name, Method: zip.Deflate, Modified: time.Now()}); err == nil {
			_, _ = f.Write(b)
		}
	}

	c.mu.Lock()
	settings := c.settings
	type nodeDiag struct {
		Name      string        `json:"name"`
		ID        string        `json:"id"`
		Online    bool          `json:"online"`
		LastSeen  *time.Time    `json:"last_seen,omitempty"`
		Rec       *NodeRec      `json:"record"`
		Heartbeat *Heartbeat    `json:"heartbeat,omitempty"`
		Update    *UpdateStatus `json:"update,omitempty"`
	}
	var nodes []nodeDiag
	var online []string
	for id, n := range c.nodes {
		d := nodeDiag{Name: displayName(n), ID: id, Rec: n, Heartbeat: c.lastHB[id], Update: c.updates[id]}
		if _, ok := c.conns[id]; ok {
			d.Online = true
			online = append(online, id)
		}
		if t, ok := c.lastSeen[id]; ok {
			t := t
			d.LastSeen = &t
		}
		nodes = append(nodes, d)
	}
	names := map[string]string{}
	for _, n := range nodes {
		names[n.ID] = n.Name
	}
	models := make([]*ModelRec, 0, len(c.models))
	for _, m := range c.models {
		models = append(models, m)
	}
	tasks := make([]*Task, 0, len(c.tasks))
	for _, t := range c.tasks {
		tasks = append(tasks, t)
	}
	c.mu.Unlock()
	sort.Slice(nodes, func(i, j int) bool { return nodes[i].Name < nodes[j].Name })

	var sum strings.Builder
	fmt.Fprintf(&sum, "Outpost diagnostics, %s\n\n", time.Now().Format(time.RFC1123))
	fmt.Fprintf(&sum, "Controller version: %s (%s, %s)\n", Version, thisPlatform(), runtime.Version())
	fmt.Fprintf(&sum, "Up since: %s (%s)\n", ctlStarted.Format(time.RFC3339), time.Since(ctlStarted).Round(time.Second))
	fmt.Fprintf(&sum, "Data folder free: %s\n", humanBytes(int64(diskFree(c.dataDir))))
	fmt.Fprintf(&sum, "Nodes: %d (%d online)\nModels: %d\nTasks: %d\n\n", len(nodes), len(online), len(models), len(tasks))
	for _, n := range nodes {
		st := "offline"
		if n.Online {
			st = "online"
		}
		v := ""
		if n.Rec != nil && n.Rec.Hello != nil {
			v = n.Rec.Hello.AgentVersion + " " + n.Rec.Hello.OS + "/" + n.Rec.Hello.Arch
		}
		fmt.Fprintf(&sum, "  %-24s %-8s %s\n", n.Name, st, v)
	}
	if notes := c.recoveredList(); len(notes) > 0 {
		fmt.Fprintf(&sum, "\nRecovered at start-up:\n  %s\n", strings.Join(notes, "\n  "))
	}
	add("summary.txt", []byte(sum.String()))
	add("controller.log", []byte(ctlLog.String()))
	add("settings.json", redactedJSON(settings))
	add("nodes.json", redactedJSON(nodes))
	add("models.json", redactedJSON(models))

	// Recent tasks: status and event log (prompts only when asked).
	sort.Slice(tasks, func(i, j int) bool { return tasks[i].Created.After(tasks[j].Created) })
	if len(tasks) > 20 {
		tasks = tasks[:20]
	}
	for _, t := range tasks {
		t.mu.Lock()
		var b strings.Builder
		fmt.Fprintf(&b, "Task %s\nTitle: %s\nStatus: %s\nPlanner: %s\nCreated: %s\nTests: %s  Build: %s  Fix rounds: %d\n",
			t.ID, t.Title, t.Status, t.Planner, t.Created.Format(time.RFC3339), t.TestStatus, t.BuildStatus, t.FixRounds)
		if t.Error != "" {
			fmt.Fprintf(&b, "Error: %s\n", t.Error)
		}
		if opt.Prompts {
			fmt.Fprintf(&b, "\nPrompt:\n%s\n", t.Prompt)
		}
		b.WriteString("\nSubtasks:\n")
		for _, s := range t.Subtasks {
			fmt.Fprintf(&b, "  %s %-10s node=%s %s\n", s.ID, s.Status, firstNonEmpty(s.NodeName, names[s.Node]), s.Error)
		}
		b.WriteString("\nEvents:\n")
		for _, e := range t.Events {
			fmt.Fprintf(&b, "  %s %-5s %s\n", e.Time.Format("01-02 15:04:05"), e.Level, e.Text)
		}
		if t.TestOutput != "" {
			fmt.Fprintf(&b, "\nLast test output:\n%s\n", t.TestOutput)
		}
		t.mu.Unlock()
		add("tasks/"+t.ID+".txt", []byte(b.String()))
	}

	// Node logs, fetched in parallel.
	if opt.NodeLogs || opt.TrayLogs {
		type got struct {
			name string
			b    []byte
		}
		var mu sync.Mutex
		var files []got
		var wg sync.WaitGroup
		for _, id := range online {
			var srcs []string
			if opt.NodeLogs {
				srcs = append(srcs, "node", "llama")
			}
			if opt.TrayLogs {
				srcs = append(srcs, "tray")
			}
			for _, src := range srcs {
				wg.Add(1)
				go func(id, src string) {
					defer wg.Done()
					var lt LogTail
					text := ""
					if err := c.ask(id, "log.tail", LogTailArgs{Source: src, Lines: 2000}, 10*time.Second, &lt); err != nil {
						text = "(could not read: " + err.Error() + ")\n"
					} else {
						text = lt.Text
						if lt.Note != "" {
							text = "(" + lt.Note + ")\n" + text
						}
					}
					mu.Lock()
					files = append(files, got{"nodes/" + safeName(names[id]) + "/" + src + ".log", []byte(text)})
					mu.Unlock()
				}(id, src)
			}
		}
		wg.Wait()
		sort.Slice(files, func(i, j int) bool { return files[i].name < files[j].name })
		for _, f := range files {
			add(f.name, f.b)
		}
	}
	return z.Close()
}

// safeName makes a node name usable as a folder name in the zip.
func safeName(s string) string {
	s = strings.Map(func(r rune) rune {
		if r == '/' || r == '\\' || r == ':' || r < 32 {
			return '_'
		}
		return r
	}, s)
	if s == "" || s == "." || s == ".." {
		return "node"
	}
	return s
}
