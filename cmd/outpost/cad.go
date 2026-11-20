package main

// 3D models as scripted CAD (New 3D model page).
//
// A chat model on a node writes an OpenSCAD file from the owner's description,
// an exec node that has OpenSCAD renders it to a binary STL, and render errors
// go back to the model for up to MaxRounds fix rounds. With Check on, the model
// also gets the rendered size and may correct the file once it sees it.
// Numeric top-level variables become parameters the owner can change and
// re-render without the model.
//
// Results live on the controller in data/cad/<id>/{meta.json,model.scad,model.stl}.
// Nodes get the .scad inside the exec job and keep nothing: the job workspace is
// removed when the STL has been fetched. Old results follow the picture
// retention days (Settings, Pictures); kept ones stay.

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"math"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	cadDefaultRounds = 4
	cadMaxRounds     = 8
	cadMaxPrompt     = 4000
	cadMaxCode       = 200 << 10
	cadRenderTimeout = 5 * time.Minute
	cadMaxParams     = 40
)

var cadTemp = 0.2 // steady code, little invention

const noOpenSCADNote = "No exec node has OpenSCAD, so nothing can be rendered. Install it on a node with exec enabled (for example `sudo apt install openscad`, `brew install --cask openscad` or the Windows installer from openscad.org); the Nodes page lists the tools each node has."

// CadParam is one numeric or true/false top-level variable of the file.
type CadParam struct {
	Name    string   `json:"name"`
	Value   string   `json:"value"` // as written in the file
	Bool    bool     `json:"bool,omitempty"`
	Min     *float64 `json:"min,omitempty"` // from a Customizer comment: // [min:max] or // [min:step:max]
	Max     *float64 `json:"max,omitempty"`
	Step    *float64 `json:"step,omitempty"`
	Comment string   `json:"comment,omitempty"`
}

// CadTurn is one ask from the owner (the first is the description).
type CadTurn struct {
	Text string    `json:"text"`
	At   time.Time `json:"at"`
}

// CadRec is one 3D model and its history.
type CadRec struct {
	ID        string            `json:"id"`
	Title     string            `json:"title"`
	Prompt    string            `json:"prompt"`
	Node      string            `json:"node"` // model node choice: "auto" or a node id
	MaxRounds int               `json:"max_rounds"`
	Check     bool              `json:"check"`
	Status    string            `json:"status"` // queued, working, done, failed, stopped
	Stage     string            `json:"stage,omitempty"`
	Round     int               `json:"round"`
	Params    []CadParam        `json:"params,omitempty"`
	Overrides map[string]string `json:"overrides,omitempty"`
	Size      [3]float64        `json:"size"` // bounding box in mm (x, y, z)
	Tris      int               `json:"tris"`
	STLBytes  int64             `json:"stl_bytes"`
	ModelName string            `json:"model_name,omitempty"`
	NodeName  string            `json:"node_name,omitempty"`
	Renderer  string            `json:"renderer,omitempty"` // node that rendered the STL
	Error     string            `json:"error,omitempty"`
	Log       []string          `json:"log,omitempty"` // one line per step, for the page
	Asks      []CadTurn         `json:"asks,omitempty"`
	Keep      bool              `json:"keep,omitempty"`
	Version   int               `json:"version"` // bumps when model.stl changes, so the page reloads it
	Created   time.Time         `json:"created"`
	Updated   time.Time         `json:"updated"`
}

func (r *CadRec) busy() bool { return r.Status == "queued" || r.Status == "working" }

type cadStore struct {
	mu      sync.Mutex
	recs    map[string]*CadRec
	cancels map[string]context.CancelFunc
	// Tests stand in for the model and the renderer.
	llmStub    func(ctx context.Context, rec *CadRec, msgs []ChatMessage) (string, error)
	renderStub func(ctx context.Context, code string, defs map[string]string) (stl []byte, errText string, err error)
}

func (c *Controller) cadDir() string { return filepath.Join(c.dataDir, "cad") }

func (c *Controller) loadCad() {
	s := &cadStore{recs: map[string]*CadRec{}, cancels: map[string]context.CancelFunc{}}
	c.cad = s
	entries, _ := os.ReadDir(c.cadDir())
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		b, err := os.ReadFile(filepath.Join(c.cadDir(), e.Name(), "meta.json"))
		if err != nil {
			continue
		}
		var rec CadRec
		if json.Unmarshal(b, &rec) != nil || rec.ID != e.Name() {
			continue
		}
		if rec.busy() {
			rec.Status, rec.Stage, rec.Error = "failed", "", "interrupted by a controller restart; ask again to continue"
		}
		s.recs[rec.ID] = &rec
	}
}

// saveCadLocked writes meta.json; the caller holds c.cad.mu.
func (c *Controller) saveCadLocked(rec *CadRec) {
	if c.cad.recs[rec.ID] != rec {
		return // deleted meanwhile
	}
	dir := filepath.Join(c.cadDir(), rec.ID)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		log.Printf("3d model %s: %v", rec.ID, err)
		return
	}
	rec.Updated = time.Now()
	b, _ := json.MarshalIndent(rec, "", "  ")
	_ = writeFileAtomic(filepath.Join(dir, "meta.json"), b, 0o644)
}

func (c *Controller) cadUpdate(rec *CadRec, f func()) {
	c.cad.mu.Lock()
	defer c.cad.mu.Unlock()
	f()
	c.saveCadLocked(rec)
}

func (c *Controller) cadLog(rec *CadRec, format string, a ...any) {
	line := fmt.Sprintf(format, a...)
	c.cadUpdate(rec, func() {
		rec.Log = append(rec.Log, time.Now().Format("15:04:05")+" "+line)
		if len(rec.Log) > 60 {
			rec.Log = rec.Log[len(rec.Log)-60:]
		}
	})
}

func (c *Controller) cadCode(id string) string {
	b, _ := os.ReadFile(filepath.Join(c.cadDir(), id, "model.scad"))
	return string(b)
}

// ---------- the OpenSCAD file ----------

var (
	cadAssignRe = regexp.MustCompile(`^\s*([A-Za-z_][A-Za-z0-9_]*)\s*=\s*([^;]+?)\s*;\s*(?://\s*(.*))?$`)
	cadNumRe    = regexp.MustCompile(`^-?(\d+\.?\d*|\.\d+)([eE][-+]?\d+)?$`)
	cadRangeRe  = regexp.MustCompile(`^\[\s*(-?[\d.]+)\s*:\s*(-?[\d.]+)\s*(?::\s*(-?[\d.]+)\s*)?\]`)
	cadFenceRe  = regexp.MustCompile("(?s)```[ \\t]*([A-Za-z0-9_-]*)[^\\n]*\\n(.*?)```")
)

// cadParams lists numeric and true/false variables assigned at the top level of
// the file (outside every brace), the ones OpenSCAD's -D can override.
func cadParams(code string) []CadParam {
	var out []CadParam
	seen := map[string]bool{}
	depth := 0
	for _, line := range strings.Split(code, "\n") {
		if depth == 0 && len(out) < cadMaxParams {
			if m := cadAssignRe.FindStringSubmatch(line); m != nil && !seen[m[1]] && !strings.HasPrefix(m[1], "$") {
				v := strings.TrimSpace(m[2])
				p := CadParam{Name: m[1], Value: v}
				ok := cadNumRe.MatchString(v)
				if v == "true" || v == "false" {
					p.Bool, ok = true, true
				}
				if ok {
					comment := strings.TrimSpace(m[3])
					if r := cadRangeRe.FindStringSubmatch(comment); r != nil {
						lo, _ := strconv.ParseFloat(r[1], 64)
						hi, _ := strconv.ParseFloat(r[2], 64)
						if r[3] != "" { // [min:step:max]
							st := hi
							hi, _ = strconv.ParseFloat(r[3], 64)
							p.Step = &st
						}
						p.Min, p.Max = &lo, &hi
						comment = strings.TrimSpace(comment[len(r[0]):])
					}
					p.Comment = shortText(comment, 120)
					seen[p.Name] = true
					out = append(out, p)
				}
			}
		}
		depth += strings.Count(stripLineComment(line), "{") - strings.Count(stripLineComment(line), "}")
		if depth < 0 {
			depth = 0
		}
	}
	return out
}

func stripLineComment(s string) string {
	if i := strings.Index(s, "//"); i >= 0 {
		return s[:i]
	}
	return s
}

// cadOverrides keeps only values -D can take safely: numbers and true/false
// for variables the file really has.
func cadOverrides(params []CadParam, in map[string]string) (map[string]string, error) {
	known := map[string]CadParam{}
	for _, p := range params {
		known[p.Name] = p
	}
	out := map[string]string{}
	for k, v := range in {
		p, ok := known[k]
		if !ok {
			return nil, fmt.Errorf("the file has no parameter %q", k)
		}
		v = strings.TrimSpace(v)
		if p.Bool {
			if v != "true" && v != "false" {
				return nil, fmt.Errorf("%s must be true or false", k)
			}
		} else if !cadNumRe.MatchString(v) {
			return nil, fmt.Errorf("%s must be a number", k)
		}
		if v != p.Value {
			out[k] = v
		}
	}
	return out, nil
}

// cadExtract takes the OpenSCAD file out of a model reply: the longest fenced
// block, or the whole reply when it has no fence and looks like code. ok is
// false for a reply with no code (for a check, "OK").
func cadExtract(reply string) (code string, ok bool) {
	for _, m := range cadFenceRe.FindAllStringSubmatch(reply, -1) {
		lang := strings.ToLower(m[1])
		if lang != "" && lang != "openscad" && lang != "scad" && lang != "c" && lang != "cpp" {
			continue
		}
		if len(m[2]) > len(code) {
			code = m[2]
		}
	}
	if code == "" {
		t := strings.TrimSpace(reply)
		if strings.Contains(t, ";") && strings.ContainsAny(t, "()") && !strings.Contains(t, "```") && len(t) > 20 {
			code = t
		}
	}
	code = strings.TrimSpace(code)
	return code, code != ""
}

// ---------- STL ----------

// stlStats reads a binary or ASCII STL and returns its bounding box size and
// triangle count.
func stlStats(b []byte) (size [3]float64, tris int, err error) {
	lo := [3]float64{math.Inf(1), math.Inf(1), math.Inf(1)}
	hi := [3]float64{math.Inf(-1), math.Inf(-1), math.Inf(-1)}
	add := func(v [3]float64) {
		for i := 0; i < 3; i++ {
			lo[i], hi[i] = math.Min(lo[i], v[i]), math.Max(hi[i], v[i])
		}
	}
	if len(b) >= 84 {
		n := int(binary.LittleEndian.Uint32(b[80:84]))
		if n >= 0 && 84+50*n == len(b) {
			for t := 0; t < n; t++ {
				off := 84 + 50*t + 12
				for k := 0; k < 3; k++ {
					var v [3]float64
					for i := 0; i < 3; i++ {
						v[i] = float64(math.Float32frombits(binary.LittleEndian.Uint32(b[off+12*k+4*i:])))
					}
					add(v)
				}
			}
			tris = n
		}
	}
	if tris == 0 && strings.HasPrefix(strings.TrimSpace(string(b[:min(len(b), 512)])), "solid") {
		verts := 0
		for _, line := range strings.Split(string(b), "\n") {
			f := strings.Fields(line)
			if len(f) == 4 && f[0] == "vertex" {
				var v [3]float64
				for i := 0; i < 3; i++ {
					v[i], err = strconv.ParseFloat(f[i+1], 64)
					if err != nil {
						return size, 0, fmt.Errorf("bad STL vertex: %q", line)
					}
				}
				add(v)
				verts++
			}
		}
		tris = verts / 3
	}
	if tris == 0 {
		return size, 0, errors.New("the STL is empty: the file makes no solid geometry")
	}
	for i := 0; i < 3; i++ {
		size[i] = math.Round((hi[i]-lo[i])*100) / 100
	}
	return size, tris, nil
}

// ---------- model and renderer ----------

const cadSystemPrompt = `You write OpenSCAD files for 3D printing and CAD. Units are millimetres; Z is up and the part sits on Z = 0.
Rules:
- Reply with exactly one complete file in a single fenced block marked openscad. No other code blocks.
- Put every main dimension as a top-level variable at the start of the file, one per line, with a Customizer range comment, for example: width = 70; // [20:200] base width
- Use only the built-in OpenSCAD language (no include or use of libraries that may not be installed, no import of outside files).
- Make one manifold solid (or a few separate solids); avoid zero-thickness walls and coincident faces in difference() by overlapping cuts by 0.01 or more.
- Set $fn to 48 to 96 for round parts.
Before the block, write at most three short sentences about the design.`

func cadAskPrompt(code, ask string) string {
	return "Here is the current OpenSCAD file:\n```openscad\n" + code + "\n```\nChange it as follows and reply with the whole new file: " + ask
}

func cadFixPrompt(code, errText string) string {
	return "OpenSCAD could not render this file:\n```openscad\n" + code + "\n```\nOpenSCAD said:\n```\n" + shortText(errText, 3000) + "\n```\nFix it and reply with the whole corrected file."
}

func cadCheckPrompt(prompt string, size [3]float64, tris int) string {
	return fmt.Sprintf("The file rendered. The part measures %.2f mm in X, %.2f mm in Y and %.2f mm in Z (bounding box) and has %d triangles.\n"+
		"Compare that with the request: %q\nIf the size and shape plausibly match, reply with only the word OK. Otherwise reply with the whole corrected file.", size[0], size[1], size[2], tris, prompt)
}

// cadLLM runs one model call on a node with a chat model loaded.
func (c *Controller) cadLLM(ctx context.Context, rec *CadRec, msgs []ChatMessage) (string, error) {
	if c.cad.llmStub != nil {
		return c.cad.llmStub(ctx, rec, msgs)
	}
	node, err := c.acquireModelNode(ctx, rec.Node, "", chatWait)
	if err != nil {
		return "", err
	}
	defer c.releaseNode(node)
	ctxTok := c.nodeCtx(node)
	maxOut := min(6144, max(1024, ctxTok/2))
	name := c.nodeLabel(node)
	live := taskLive.begin("cad_"+rec.ID, "", "3D model", name, false)
	defer live.End()
	out, err := c.runInferenceJobLive(ctx, node, InferenceSpec{Messages: fitHistory(msgs, max(2000, (ctxTok-maxOut)*3)), Params: InferenceParams{MaxTokens: maxOut, Temperature: &cadTemp}}, "3D model", chatTimeout, live)
	if err != nil {
		return "", err
	}
	c.cadUpdate(rec, func() { rec.NodeName, rec.ModelName = name, out.Model })
	if out.FinishReason == "length" {
		return out.Text, errors.New("the model's reply was cut off at the length limit; try a simpler description or a model with more context")
	}
	return out.Text, nil
}

// openscadNode picks an exec node that reports OpenSCAD.
func (c *Controller) openscadNode() (string, ToolInfo, bool) {
	for _, id := range c.execNodes() {
		tools, known := c.nodeTools(id)
		if !known {
			continue
		}
		for _, tl := range tools {
			if tl.Name == "openscad" {
				return id, tl, true
			}
		}
	}
	return "", ToolInfo{}, false
}

// cadRenderNames lists the online exec nodes with OpenSCAD, for the page.
func (c *Controller) cadRenderNames() []string {
	var out []string
	for _, id := range c.execNodes() {
		if tools, known := c.nodeTools(id); known && hasTool(tools, "openscad") {
			out = append(out, c.nodeLabel(id))
		}
	}
	return out
}

func cadRenderArgv(bin string, defs map[string]string, exportFormat bool) []string {
	argv := []string{bin, "-o", "model.stl"}
	if exportFormat {
		argv = append(argv, "--export-format", "binstl")
	}
	keys := make([]string, 0, len(defs))
	for k := range defs {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		argv = append(argv, "-D", k+"="+defs[k])
	}
	return append(argv, "model.scad")
}

// cadRender renders the file on an OpenSCAD node. errText is OpenSCAD's
// complaint when the file is at fault (the model can fix it); err is for
// everything else.
func (c *Controller) cadRender(ctx context.Context, rec *CadRec, code string, defs map[string]string) (stl []byte, errText string, err error) {
	if c.cad.renderStub != nil {
		return c.cad.renderStub(ctx, code, defs)
	}
	node, tool, ok := c.openscadNode()
	if !ok {
		return nil, "", errors.New(noOpenSCADNote)
	}
	bin := tool.Path
	if bin == "" {
		bin = tool.Cmd
	}
	c.cadUpdate(rec, func() { rec.Renderer = c.nodeLabel(node) })
	data := []byte(code)
	sum := sha256.Sum256(data)
	inputs := []FileEntry{{Path: "model.scad", SHA256: hex.EncodeToString(sum[:]), Bytes: int64(len(data)), Mode: "0644", InlineB64: base64.StdEncoding.EncodeToString(data)}}
	for _, exportFormat := range []bool{true, false} {
		out, res, err := c.runExecJobFiles(ctx, node, cadRenderArgv(bin, defs, exportFormat), inputs, nil, []string{"model.stl"}, rec.Title+" · render", cadRenderTimeout)
		if err != nil {
			c.releaseJobFiles(node, res)
			return nil, "", fmt.Errorf("render on %s: %w", c.nodeLabel(node), err)
		}
		msg := strings.TrimSpace(out.StderrTail + "\n" + out.StdoutTail)
		if out.ExitCode != 0 && exportFormat && strings.Contains(msg, "export-format") {
			c.releaseJobFiles(node, res)
			continue // OpenSCAD before 2021 has no --export-format; it writes ASCII STL
		}
		if out.ExitCode != 0 || len(res.Files) == 0 {
			c.releaseJobFiles(node, res)
			if msg == "" {
				msg = fmt.Sprintf("OpenSCAD exited with code %d and wrote no STL", out.ExitCode)
			}
			if out.SandboxDenied {
				return nil, "", fmt.Errorf("the exec sandbox on %s blocked OpenSCAD: %s", c.nodeLabel(node), firstLine(msg, 300))
			}
			return nil, msg, nil
		}
		tmp, err := os.MkdirTemp(c.dataDir, ".cad-")
		if err != nil {
			c.releaseJobFiles(node, res)
			return nil, "", err
		}
		defer os.RemoveAll(tmp)
		if _, err := c.fetchJobFiles(ctx, node, res.JobID, res.Files, tmp, nil); err != nil {
			return nil, "", err
		}
		b, err := os.ReadFile(filepath.Join(tmp, "model.stl"))
		if err != nil {
			return nil, "", err
		}
		if strings.Contains(msg, "WARNING: No top level geometry") || strings.Contains(msg, "Current top level object is empty") {
			return nil, msg, nil
		}
		return b, "", nil
	}
	return nil, "", errors.New("OpenSCAD did not render")
}

// ---------- the run ----------

type cadJob struct {
	kind string // "new", "ask", "params"
	ask  string
}

func (c *Controller) startCad(rec *CadRec, job cadJob) {
	ctx, cancel := context.WithCancel(context.Background())
	c.cad.mu.Lock()
	c.cad.cancels[rec.ID] = cancel
	rec.Status, rec.Error, rec.Stage, rec.Round = "working", "", "starting", 0
	c.saveCadLocked(rec)
	c.cad.mu.Unlock()
	go func() {
		defer cancel()
		err := c.runCad(ctx, rec, job)
		c.cad.mu.Lock()
		delete(c.cad.cancels, rec.ID)
		rec.Stage = ""
		switch {
		case errors.Is(ctx.Err(), context.Canceled):
			rec.Status, rec.Error = "stopped", ""
		case err != nil:
			rec.Status, rec.Error = "failed", err.Error()
		default:
			rec.Status = "done"
		}
		c.saveCadLocked(rec)
		c.cad.mu.Unlock()
	}()
}

func (c *Controller) runCad(ctx context.Context, rec *CadRec, job cadJob) error {
	c.cad.mu.Lock()
	prompt, maxRounds, check := rec.Prompt, rec.MaxRounds, rec.Check
	defs := rec.Overrides
	c.cad.mu.Unlock()
	code := c.cadCode(rec.ID)
	stage := func(s string) { c.cadUpdate(rec, func() { rec.Stage = s }) }

	if job.kind == "params" {
		stage("rendering")
		stl, errText, err := c.cadRender(ctx, rec, code, defs)
		if err != nil {
			return err
		}
		if errText != "" {
			return errors.New("OpenSCAD could not render with these values: " + firstLine(errText, 400))
		}
		return c.cadSaveResult(rec, code, stl, false)
	}

	msgs := []ChatMessage{{Role: "system", Content: cadSystemPrompt}}
	if job.kind == "ask" && code != "" {
		// The model sees the values the owner is looking at; they become the file's own.
		code, defs = cadApplyOverrides(code, defs), nil
		msgs = append(msgs, ChatMessage{Role: "user", Content: "Original request: " + prompt + "\n\n" + cadAskPrompt(code, job.ask)})
		c.cadLog(rec, "Asked for a change: %s", shortText(job.ask, 200))
	} else {
		msgs = append(msgs, ChatMessage{Role: "user", Content: prompt})
	}
	checked := !check
	good := false // an STL from this run is stored
	pending := "" // a corrected file to render without another model call
	for round := 1; round <= maxRounds; round++ {
		c.cadUpdate(rec, func() { rec.Round = round })
		if pending != "" {
			code, pending = pending, ""
		} else {
			stage("writing the file")
			if round > 1 {
				stage("fixing")
			}
			reply, err := c.cadLLM(ctx, rec, msgs)
			if err != nil {
				if good {
					c.cadLog(rec, "The model call failed (%v); keeping the last good render.", err)
					return nil
				}
				return err
			}
			next, ok := cadExtract(reply)
			if !ok {
				c.cadLog(rec, "Round %d: the reply had no OpenSCAD file; asking again.", round)
				msgs = append(msgs, ChatMessage{Role: "assistant", Content: reply}, ChatMessage{Role: "user", Content: "Reply with the whole OpenSCAD file in one ```openscad block."})
				continue
			}
			if len(next) > cadMaxCode {
				return fmt.Errorf("the file is over %s; ask for a simpler part", humanBytes(cadMaxCode))
			}
			code = next
			msgs = append(msgs, ChatMessage{Role: "assistant", Content: "```openscad\n" + code + "\n```"})
		}
		params := cadParams(code)
		var keep map[string]string // a new file starts from its own values
		stage("rendering")
		stl, errText, err := c.cadRender(ctx, rec, code, keep)
		if err != nil {
			return err
		}
		if errText != "" {
			c.cadLog(rec, "Round %d: OpenSCAD failed: %s", round, firstLine(errText, 200))
			if good {
				if round >= maxRounds {
					c.cadLog(rec, "Keeping the last good render.")
					return nil
				}
			} else if round >= maxRounds {
				_ = c.cadSaveCode(rec, code, params, keep)
				return errors.New("OpenSCAD still fails after " + strconv.Itoa(round) + " rounds: " + firstLine(errText, 300))
			}
			msgs = append(msgs, ChatMessage{Role: "user", Content: cadFixPrompt(code, errText)})
			continue
		}
		size, tris, _ := stlStats(stl)
		c.cadLog(rec, "Round %d: rendered %.1f × %.1f × %.1f mm, %d triangles.", round, size[0], size[1], size[2], tris)
		c.cadUpdate(rec, func() { rec.Overrides = keep })
		if err := c.cadSaveResult(rec, code, stl, true); err != nil {
			return err
		}
		good = true
		if checked || round >= maxRounds {
			return nil
		}
		checked = true
		stage("checking the size")
		check := ChatMessage{Role: "user", Content: cadCheckPrompt(prompt+cadAsksSuffix(rec), size, tris)}
		reply, err := c.cadLLM(ctx, rec, append(msgs, check))
		if err != nil {
			c.cadLog(rec, "The size check could not run: %v", err)
			return nil // the rendered model stands
		}
		fixed, ok := cadExtract(reply)
		if !ok || fixed == code {
			c.cadLog(rec, "The model checked the size: OK.")
			return nil
		}
		c.cadLog(rec, "The model corrected the file after seeing the size.")
		msgs = append(msgs, check, ChatMessage{Role: "assistant", Content: "```openscad\n" + fixed + "\n```"})
		pending = fixed
	}
	if good {
		return nil
	}
	return fmt.Errorf("no usable OpenSCAD file after %d rounds", maxRounds)
}

// cadAsksSuffix adds the owner's later asks to the request text for the size check.
func cadAsksSuffix(rec *CadRec) string {
	var b strings.Builder
	for i, a := range rec.Asks {
		if i > 0 {
			b.WriteString("; then: " + a.Text)
		}
	}
	return b.String()
}

// cadApplyOverrides writes override values into the file's top-level assignments.
func cadApplyOverrides(code string, defs map[string]string) string {
	if len(defs) == 0 {
		return code
	}
	lines := strings.Split(code, "\n")
	depth := 0
	for i, line := range lines {
		if depth == 0 {
			if m := cadAssignRe.FindStringSubmatchIndex(line); m != nil {
				if v, ok := defs[line[m[2]:m[3]]]; ok {
					lines[i] = line[:m[4]] + v + line[m[5]:]
				}
			}
		}
		depth = max(0, depth+strings.Count(stripLineComment(line), "{")-strings.Count(stripLineComment(line), "}"))
	}
	return strings.Join(lines, "\n")
}

func (c *Controller) cadSaveCode(rec *CadRec, code string, params []CadParam, defs map[string]string) error {
	dir := filepath.Join(c.cadDir(), rec.ID)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	if err := writeFileAtomic(filepath.Join(dir, "model.scad"), []byte(code), 0o644); err != nil {
		return err
	}
	c.cadUpdate(rec, func() { rec.Params, rec.Overrides = params, defs })
	return nil
}

// cadSaveResult saves the file and its STL and updates the size.
func (c *Controller) cadSaveResult(rec *CadRec, code string, stl []byte, newCode bool) error {
	size, tris, err := stlStats(stl)
	if err != nil {
		return err
	}
	dir := filepath.Join(c.cadDir(), rec.ID)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	if newCode {
		if err := writeFileAtomic(filepath.Join(dir, "model.scad"), []byte(code), 0o644); err != nil {
			return err
		}
	}
	if err := writeFileAtomic(filepath.Join(dir, "model.stl"), stl, 0o644); err != nil {
		return err
	}
	params := cadParams(code)
	c.cadUpdate(rec, func() {
		rec.Size, rec.Tris, rec.STLBytes = size, tris, int64(len(stl))
		rec.Params = params
		rec.Version++
	})
	return nil
}

// ---------- retention ----------

func (c *Controller) cadLoop() {
	t := time.NewTicker(time.Hour)
	defer t.Stop()
	for range t.C {
		c.pruneCad()
	}
}

// pruneCad deletes finished, unkept models older than the picture retention days.
func (c *Controller) pruneCad() {
	days := genDefaultDays
	if c.gens != nil {
		c.gens.mu.Lock()
		days = c.gens.settings.RetentionDays
		c.gens.mu.Unlock()
	}
	if days <= 0 {
		return
	}
	cut := time.Now().AddDate(0, 0, -days)
	var del []string
	c.cad.mu.Lock()
	for id, r := range c.cad.recs {
		if !r.Keep && !r.busy() && r.Updated.Before(cut) {
			del = append(del, id)
		}
	}
	c.cad.mu.Unlock()
	for _, id := range del {
		c.deleteCad(id)
	}
	if len(del) > 0 {
		log.Printf("3d models: removed %d old model(s) under the retention settings", len(del))
	}
}

func (c *Controller) deleteCad(id string) {
	c.cad.mu.Lock()
	if cancel := c.cad.cancels[id]; cancel != nil {
		cancel()
	}
	delete(c.cad.recs, id)
	c.cad.mu.Unlock()
	os.RemoveAll(filepath.Join(c.cadDir(), id))
}

// ---------- HTTP ----------

func (c *Controller) cadRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/cad", c.auth(c.handleListCad))
	mux.HandleFunc("POST /api/cad", c.auth(c.handleNewCad))
	mux.HandleFunc("GET /api/cad/{id}", c.auth(c.handleGetCad))
	mux.HandleFunc("POST /api/cad/{id}/ask", c.auth(c.handleCadAsk))
	mux.HandleFunc("POST /api/cad/{id}/params", c.auth(c.handleCadParams))
	mux.HandleFunc("POST /api/cad/{id}/stop", c.auth(c.handleCadStop))
	mux.HandleFunc("POST /api/cad/{id}/keep", c.auth(c.handleCadKeep))
	mux.HandleFunc("DELETE /api/cad/{id}", c.auth(c.handleDeleteCad))
	mux.HandleFunc("GET /api/cad/{id}/{file}", c.auth(c.handleCadFile))
}

type cadModelNode struct {
	ID    string `json:"id"`
	Name  string `json:"name"`
	Model string `json:"model"`
}

func (c *Controller) handleListCad(w http.ResponseWriter, r *http.Request) {
	var nodes []cadModelNode
	for _, id := range c.workerNodes() {
		c.mu.Lock()
		model := ""
		if hb := c.lastHB[id]; hb != nil {
			model = hb.LoadedModel
		}
		c.mu.Unlock()
		nodes = append(nodes, cadModelNode{ID: id, Name: c.nodeLabel(id), Model: model})
	}
	render := c.cadRenderNames()
	c.cad.mu.Lock()
	list := make([]CadRec, 0, len(c.cad.recs))
	for _, rec := range c.cad.recs {
		v := *rec
		v.Log = nil
		list = append(list, v)
	}
	c.cad.mu.Unlock()
	sort.Slice(list, func(i, j int) bool { return list[i].Created.After(list[j].Created) })
	note := ""
	if len(render) == 0 {
		note = noOpenSCADNote
	}
	writeJSON(w, 200, map[string]any{"models": list, "model_nodes": nodes, "render_nodes": render, "note": note})
}

func (c *Controller) cadOr404(w http.ResponseWriter, r *http.Request) *CadRec {
	c.cad.mu.Lock()
	rec := c.cad.recs[r.PathValue("id")]
	c.cad.mu.Unlock()
	if rec == nil {
		writeJSON(w, 404, map[string]string{"error": "no such 3D model"})
	}
	return rec
}

func (c *Controller) writeCad(w http.ResponseWriter, code int, rec *CadRec) {
	c.cad.mu.Lock()
	v := *rec
	c.cad.mu.Unlock()
	writeJSON(w, code, map[string]any{"model": v, "code": c.cadCode(rec.ID)})
}

func (c *Controller) handleGetCad(w http.ResponseWriter, r *http.Request) {
	if rec := c.cadOr404(w, r); rec != nil {
		c.writeCad(w, 200, rec)
	}
}

type cadNewReq struct {
	Prompt string `json:"prompt"`
	Node   string `json:"node"`
	Rounds int    `json:"rounds"`
	Check  *bool  `json:"check"`
}

func (c *Controller) handleNewCad(w http.ResponseWriter, r *http.Request) {
	var req cadNewReq
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10)).Decode(&req); err != nil {
		writeJSON(w, 400, map[string]string{"error": "bad request"})
		return
	}
	req.Prompt = strings.TrimSpace(req.Prompt)
	switch {
	case req.Prompt == "":
		writeJSON(w, 400, map[string]string{"error": "describe the part first"})
		return
	case len(req.Prompt) > cadMaxPrompt:
		writeJSON(w, 400, map[string]string{"error": "the description is too long"})
		return
	}
	if c.cad.renderStub == nil {
		if _, _, ok := c.openscadNode(); !ok {
			writeJSON(w, 409, map[string]string{"error": noOpenSCADNote})
			return
		}
	}
	if req.Node == "" {
		req.Node = "auto"
	}
	if req.Rounds <= 0 {
		req.Rounds = cadDefaultRounds
	}
	now := time.Now()
	rec := &CadRec{ID: newID("cad"), Title: chatTitle(req.Prompt), Prompt: req.Prompt, Node: req.Node, MaxRounds: min(req.Rounds, cadMaxRounds),
		Check: req.Check == nil || *req.Check, Status: "queued", Asks: []CadTurn{{Text: req.Prompt, At: now}}, Created: now}
	c.cad.mu.Lock()
	c.cad.recs[rec.ID] = rec
	c.saveCadLocked(rec)
	c.cad.mu.Unlock()
	c.startCad(rec, cadJob{kind: "new"})
	c.writeCad(w, 201, rec)
}

func (c *Controller) handleCadAsk(w http.ResponseWriter, r *http.Request) {
	rec := c.cadOr404(w, r)
	if rec == nil {
		return
	}
	var req struct {
		Text string `json:"text"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10)).Decode(&req); err != nil || strings.TrimSpace(req.Text) == "" || len(req.Text) > cadMaxPrompt {
		writeJSON(w, 400, map[string]string{"error": "say what to change"})
		return
	}
	c.cad.mu.Lock()
	if rec.busy() {
		c.cad.mu.Unlock()
		writeJSON(w, 409, map[string]string{"error": "this model is still being worked on"})
		return
	}
	rec.Asks = append(rec.Asks, CadTurn{Text: strings.TrimSpace(req.Text), At: time.Now()})
	c.cad.mu.Unlock()
	kind := "ask"
	if c.cadCode(rec.ID) == "" {
		kind = "new" // the first run never produced a file: start over
	}
	c.startCad(rec, cadJob{kind: kind, ask: strings.TrimSpace(req.Text)})
	c.writeCad(w, 202, rec)
}

func (c *Controller) handleCadParams(w http.ResponseWriter, r *http.Request) {
	rec := c.cadOr404(w, r)
	if rec == nil {
		return
	}
	var req struct {
		Values map[string]string `json:"values"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10)).Decode(&req); err != nil {
		writeJSON(w, 400, map[string]string{"error": "bad request"})
		return
	}
	c.cad.mu.Lock()
	if rec.busy() {
		c.cad.mu.Unlock()
		writeJSON(w, 409, map[string]string{"error": "this model is still being worked on"})
		return
	}
	defs, err := cadOverrides(rec.Params, req.Values)
	if err != nil {
		c.cad.mu.Unlock()
		writeJSON(w, 400, map[string]string{"error": err.Error()})
		return
	}
	rec.Overrides = defs
	c.cad.mu.Unlock()
	c.startCad(rec, cadJob{kind: "params"})
	c.writeCad(w, 202, rec)
}

func (c *Controller) handleCadStop(w http.ResponseWriter, r *http.Request) {
	rec := c.cadOr404(w, r)
	if rec == nil {
		return
	}
	c.cad.mu.Lock()
	cancel := c.cad.cancels[rec.ID]
	c.cad.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	writeJSON(w, 200, map[string]bool{"ok": true})
}

func (c *Controller) handleCadKeep(w http.ResponseWriter, r *http.Request) {
	rec := c.cadOr404(w, r)
	if rec == nil {
		return
	}
	var req struct {
		Keep bool `json:"keep"`
	}
	_ = json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<10)).Decode(&req)
	c.cadUpdate(rec, func() { rec.Keep = req.Keep })
	writeJSON(w, 200, map[string]bool{"keep": req.Keep})
}

func (c *Controller) handleDeleteCad(w http.ResponseWriter, r *http.Request) {
	if rec := c.cadOr404(w, r); rec != nil {
		c.deleteCad(rec.ID)
		writeJSON(w, 200, map[string]bool{"ok": true})
	}
}

// handleCadFile serves model.stl or model.scad; ?download=1 names the file after the title.
func (c *Controller) handleCadFile(w http.ResponseWriter, r *http.Request) {
	rec := c.cadOr404(w, r)
	if rec == nil {
		return
	}
	name := r.PathValue("file")
	media := map[string]string{"model.stl": "model/stl", "model.scad": "text/plain; charset=utf-8"}[name]
	if media == "" {
		writeJSON(w, 404, map[string]string{"error": "no such file"})
		return
	}
	b, err := os.ReadFile(filepath.Join(c.cadDir(), rec.ID, name))
	if err != nil {
		writeJSON(w, 404, map[string]string{"error": "not rendered yet"})
		return
	}
	w.Header().Set("Content-Type", media)
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Cache-Control", "no-store")
	if r.URL.Query().Get("download") == "1" {
		base := slug(rec.Title)
		if base == "" {
			base = "model"
		}
		w.Header().Set("Content-Disposition", `attachment; filename="`+base+filepath.Ext(name)+`"`)
	}
	w.Write(b)
}
