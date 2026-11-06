package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"image"
	"image/png"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/coder/websocket"
)

func genTestPNG(t testing.TB) []byte {
	var b bytes.Buffer
	if err := png.Encode(&b, image.NewRGBA(image.Rect(0, 0, 8, 8))); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

func TestImageFit(t *testing.T) {
	flux := imageModel("flux1-schnell-q8")
	sdxl := imageModel("sdxl-turbo")
	gb := int64(1 << 30)
	cases := []struct {
		name      string
		d         *ImageModelDef
		vram, ram int64
		fit       string
		offload   bool
	}{
		{"flux on a 16 GB card with 64 GB RAM: text encoders wait in RAM", flux, 16 * gb, 64 * gb, fitSplit, true},
		{"flux on a 24 GB card: everything fits", flux, 32 * gb, 64 * gb, fitVRAM, false},
		{"flux on an 8 GB card does not fit", flux, 8 * gb, 64 * gb, fitNo, false},
		{"flux on a CPU-only 64 GB server", flux, 0, 64 * gb, fitRAM, false},
		{"flux on a 4 GB VM", flux, 0, 4 * gb, fitNo, false},
		{"sdxl-turbo on a 12 GB card", sdxl, 12 * gb, 16 * gb, fitVRAM, false},
		{"sdxl-turbo is one file, so there is nothing to offload on 8 GB", sdxl, 8 * gb, 16 * gb, fitNo, false},
	}
	for _, tc := range cases {
		fit, off := imageFit(tc.d, 1024, 1024, 0, tc.vram, tc.ram)
		if fit != tc.fit || off != tc.offload {
			t.Errorf("%s: got %s offload=%v, want %s offload=%v", tc.name, fit, off, tc.fit, tc.offload)
		}
	}
	// A bigger picture needs more memory; a measured compute buffer replaces the guess.
	small, _ := estimateImage(flux, 512, 512, 0)
	big, _ := estimateImage(flux, 1536, 1536, 0)
	if big <= small {
		t.Fatal("a bigger picture should need more memory")
	}
	if f, _ := estimateImage(flux, 1024, 1024, 100<<20); f >= big {
		t.Fatal("an observed compute buffer should replace the estimate")
	}
}

func TestSDVariantAndAssets(t *testing.T) {
	gpu := []GPUInfo{{Vendor: "amd", VRAMBytes: 16 << 30}}
	if v, _ := sdVariant(&Hello{OS: "linux", Arch: "amd64", GPUs: gpu, VulkanAvailable: true}); v != "linux-x64-vulkan" {
		t.Fatalf("got %s", v)
	}
	if v, note := sdVariant(&Hello{OS: "linux", Arch: "amd64", GPUs: gpu}); v != "linux-x64-cpu" || note == "" {
		t.Fatalf("no vulkan driver: %s %q", v, note)
	}
	if v, _ := sdVariant(&Hello{OS: "linux", Arch: "arm64"}); v != "" {
		t.Fatal("no Linux ARM build exists")
	}
	if v, _ := sdVariant(&Hello{OS: "windows", Arch: "amd64", GPUs: gpu}); v != "win-x64-vulkan" {
		t.Fatalf("got %s", v)
	}
	names := map[string][]string{
		"linux-x64-vulkan": {"sd-master-abc1234-bin-Linux-Ubuntu-24.04-x86_64-vulkan.zip"},
		"linux-x64-cpu":    {"sd-master-abc1234-bin-Linux-Ubuntu-24.04-x86_64.zip"},
		"win-x64-vulkan":   {"sd-master-abc1234-bin-win-vulkan-x64.zip"},
		"win-x64-cpu":      {"sd-master-abc1234-bin-win-cpu-x64.zip"},
		"macos":            {"sd-master-abc1234-bin-Darwin-macOS-15.6.1-arm64.zip"},
	}
	all := []string{"sd-master-abc1234-bin-win-cuda12-x64.zip", "cudart-sd-bin-win-cu12-x64.zip", "sd-master-abc1234-bin-win-rocm-6.2-x64.zip",
		"sd-master-abc1234-bin-Linux-Ubuntu-24.04-x86_64-rocm-6.2.zip", "SHA256SUMS.txt"}
	for _, v := range names {
		all = append(all, v...)
	}
	for variant, want := range names {
		var got []string
		for _, n := range all {
			if sdAssetMatch(n, variant) {
				got = append(got, n)
			}
		}
		if strings.Join(got, ",") != strings.Join(want, ",") {
			t.Errorf("%s matched %v, want %v", variant, got, want)
		}
	}
}

func TestPickRepoFile(t *testing.T) {
	paths := []string{"README.md", "flux1-schnell-Q4_0.gguf", "flux1-schnell-Q8_0.gguf", "flux1-schnell-F16.gguf", "split_files/vae/ae.safetensors", "ae.safetensors.json"}
	if p, err := pickRepoFile(paths, "q8_0.gguf"); err != nil || p != "flux1-schnell-Q8_0.gguf" {
		t.Fatalf("%q %v", p, err)
	}
	if p, err := pickRepoFile(paths, "AE.safetensors"); err != nil || p != "split_files/vae/ae.safetensors" {
		t.Fatalf("exact base name: %q %v", p, err)
	}
	if _, err := pickRepoFile(paths, ".gguf"); err == nil || !strings.Contains(err.Error(), "3 files match") {
		t.Fatalf("an ambiguous match must be an error, got %v", err)
	}
	if _, err := pickRepoFile(paths, "q5_k"); err == nil {
		t.Fatal("no match must be an error")
	}
}

func TestGenSpecAndArgs(t *testing.T) {
	ok := GenSpec{Kind: genImage, Prompt: "a cat", Width: 1024, Height: 768, Steps: 4, CFG: 1, Sampler: "euler", Files: []GenArg{{Flag: "--vae", File: "ae.safetensors"}, {Flag: "--diffusion-model", File: "m.gguf"}}}
	if err := ok.validate(); err != nil {
		t.Fatal(err)
	}
	bad := []func(*GenSpec){
		func(s *GenSpec) { s.Prompt = "  " },
		func(s *GenSpec) { s.Width = 100 },
		func(s *GenSpec) { s.Width = 1001 },
		func(s *GenSpec) { s.Height = 4096 },
		func(s *GenSpec) { s.Steps = 0 },
		func(s *GenSpec) { s.Sampler = "evil; rm -rf" },
		func(s *GenSpec) { s.Files = []GenArg{{Flag: "--listen-ip", File: "x"}} },
		func(s *GenSpec) { s.Files = []GenArg{{Flag: "-m", File: "../../etc/passwd"}} },
	}
	for i, f := range bad {
		s := ok
		f(&s)
		if s.validate() == nil {
			t.Errorf("case %d should be refused", i)
		}
	}
	args := sdServerArgs(ok, "/data/models/img-x", 4000)
	want := "--listen-ip 127.0.0.1 --listen-port 4000 --diffusion-model /data/models/img-x/m.gguf --vae /data/models/img-x/ae.safetensors"
	if strings.Join(args, " ") != want {
		t.Fatalf("args %q", strings.Join(args, " "))
	}
	ok.Offload = true
	if got := sdServerArgs(ok, "/d", 1); got[len(got)-1] != "--offload-to-cpu" {
		t.Fatalf("offload missing: %v", got)
	}
}

func TestPeakCompute(t *testing.T) {
	log := "[DEBUG] flux compute buffer size: 650.00 MB(VRAM) on Vulkan0\n[DEBUG] vae compute buffer size: 1.50 GB(VRAM)\n"
	if got := peakCompute(log); got != int64(1.5*(1<<30)) {
		t.Fatalf("got %d", got)
	}
}

// fakeSD is a stand-in sd-server: it accepts a job, reports two steps, then completes.
func fakeSD(t *testing.T, img []byte, bodies chan<- map[string]any) *httptest.Server {
	var polls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/sdcpp/v1/img_gen":
			var body map[string]any
			json.NewDecoder(r.Body).Decode(&body)
			if bodies != nil {
				bodies <- body
			}
			w.WriteHeader(202)
			io.WriteString(w, `{"id":"job_1","kind":"img_gen","status":"queued"}`)
		case r.URL.Path == "/sdcpp/v1/jobs/job_1":
			if polls.Add(1) == 1 {
				io.WriteString(w, `{"id":"job_1","status":"generating","preview":{"step":2,"total_steps":4}}`)
				return
			}
			b, _ := json.Marshal(map[string]any{"id": "job_1", "status": "completed", "result": map[string]any{"images": []map[string]any{{"index": 0, "b64_json": base64.StdEncoding.EncodeToString(img)}}}})
			w.Write(b)
		default:
			w.WriteHeader(404)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestSDGenerateAgainstFakeServer(t *testing.T) {
	img := genTestPNG(t)
	bodies := make(chan map[string]any, 1)
	srv := fakeSD(t, img, bodies)
	n := &Node{}
	n.sd = &sdMgr{n: n, testURL: srv.URL}
	spec := GenSpec{Kind: genImage, Prompt: "a cat", Negative: "blurry", Width: 512, Height: 512, Steps: 4, CFG: 1, Sampler: "euler", Seed: 42}
	got, e := n.sd.generate(context.Background(), "job_x", n, spec)
	if e != nil {
		t.Fatal(e)
	}
	if !bytes.Equal(got, img) {
		t.Fatal("the picture changed on the way")
	}
	body := <-bodies
	sp := body["sample_params"].(map[string]any)
	if body["prompt"] != "a cat" || body["seed"].(float64) != 42 || sp["sample_steps"].(float64) != 4 || sp["sample_method"] != "euler" || body["batch_count"].(float64) != 1 {
		t.Fatalf("request body %v", body)
	}
}

func TestSDGenerateCancel(t *testing.T) {
	var cancelled atomic.Bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/sdcpp/v1/img_gen":
			w.WriteHeader(202)
			io.WriteString(w, `{"id":"job_1"}`)
		case strings.HasSuffix(r.URL.Path, "/cancel"):
			cancelled.Store(true)
		default:
			io.WriteString(w, `{"id":"job_1","status":"generating"}`)
		}
	}))
	defer srv.Close()
	n := &Node{}
	n.sd = &sdMgr{n: n, testURL: srv.URL}
	ctx, cancel := context.WithCancel(context.Background())
	go func() { time.Sleep(200 * time.Millisecond); cancel() }()
	_, e := n.sd.generate(ctx, "j", n, GenSpec{Prompt: "x", Width: 512, Height: 512, Steps: 1})
	if e == nil || e.Code != "cancelled" || !cancelled.Load() {
		t.Fatalf("want a cancelled error and a cancel request to sd-server, got %v cancelled=%v", e, cancelled.Load())
	}
}

// ---------- controller ----------

func genController(t *testing.T) *Controller {
	c := chatController(t)
	c.runtimes = map[string]*RuntimeRec{}
	c.conns["n"] = &nodeConn{} // online; fakeGenNode swaps in a live connection
	c.loadGens()
	c.gens.prepStub = func(context.Context, string, *ImageModelDef) error { return nil }
	c.gens.argsStub = func(d *ImageModelDef) []GenArg {
		out := make([]GenArg, len(d.Files))
		for i, f := range d.Files {
			out[i] = GenArg{Flag: f.Flag, File: "file" + string(rune('a'+i))}
		}
		return out
	}
	c.nodes["n"].Hello = &Hello{OS: "linux", Arch: "amd64", VulkanAvailable: true, RAMBytes: 64 << 30,
		GPUs:     []GPUInfo{{Vendor: "amd", VRAMBytes: 16 << 30}},
		Features: []string{"node.gen"}, SDRuntime: &RuntimeInfo{Build: "b", Variant: "linux-x64-vulkan"}}
	return c
}

// fakeGenNode answers generate jobs with a PNG, after one progress report.
func fakeGenNode(t *testing.T, c *Controller, id string, png []byte, specs *[]GenSpec, mu *sync.Mutex, fail func(GenSpec) *ErrInfo) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		c.mu.Lock()
		c.conns[id] = &nodeConn{conn: conn}
		c.mu.Unlock()
		<-r.Context().Done()
	}))
	t.Cleanup(srv.Close)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	ws, _, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(srv.URL, "http"), nil)
	if err != nil {
		t.Fatal(err)
	}
	ws.SetReadLimit(64 << 20)
	go func() {
		for {
			_, b, err := ws.Read(ctx)
			if err != nil {
				return
			}
			var env Envelope
			json.Unmarshal(b, &env)
			if env.Type != "job.dispatch" {
				continue
			}
			var jd JobDispatch
			json.Unmarshal(env.Body, &jd)
			var spec GenSpec
			json.Unmarshal(jd.Spec, &spec)
			mu.Lock()
			*specs = append(*specs, spec)
			mu.Unlock()
			o, _ := json.Marshal(JobOutput{JobID: jd.JobID, Stream: "progress", Data: "2 4\n"})
			c.handleNodeMessage(id, &Envelope{Type: "job.output", Body: o})
			var res JobResult
			if e := fail(spec); e != nil {
				res = JobResult{JobID: jd.JobID, Status: "failed", Error: e}
			} else {
				out, _ := json.Marshal(GenOutput{Seed: spec.Seed, Seconds: 1.5, Format: "png", ComputeBytes: 700 << 20})
				res = JobResult{JobID: jd.JobID, Status: "succeeded", Output: out, Files: []FileEntry{inlineFile("out.png", png)}}
			}
			rb, _ := json.Marshal(res)
			c.handleNodeMessage(id, &Envelope{Type: "job.result", Body: rb})
		}
	}()
	for i := 0; i < 100; i++ {
		c.mu.Lock()
		ok := c.conns[id] != nil
		c.mu.Unlock()
		if ok {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("fake node did not connect")
}

func genMux(c *Controller) *http.ServeMux {
	m := http.NewServeMux()
	m.HandleFunc("GET /api/images/models", c.handleImageModels)
	m.HandleFunc("POST /api/images/models/{id}/accept", c.handleImageAccept)
	m.HandleFunc("GET /api/images/settings", c.handleImageSettings)
	m.HandleFunc("POST /api/images/settings", c.handleImageSettings)
	m.HandleFunc("GET /api/images", c.handleListGens)
	m.HandleFunc("POST /api/images", c.handleCreateGen)
	m.HandleFunc("GET /api/images/{id}", c.handleGetGen)
	m.HandleFunc("GET /api/images/{id}/files/{index}", c.handleGenFile)
	m.HandleFunc("POST /api/images/{id}/cancel", c.handleCancelGen)
	m.HandleFunc("POST /api/images/{id}/retry", c.handleRetryGen)
	m.HandleFunc("POST /api/images/{id}/keep", c.handleKeepGen)
	m.HandleFunc("DELETE /api/images/{id}", c.handleDeleteGen)
	return m
}

func waitGen(t *testing.T, c *Controller, id string, want string) *GenRec {
	for i := 0; i < 300; i++ {
		c.gens.mu.Lock()
		rec := c.gens.recs[id]
		st := rec.status()
		c.gens.mu.Unlock()
		if st == want {
			return rec
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("generation never became %s", want)
	return nil
}

func TestGenerationEndToEnd(t *testing.T) {
	c := genController(t)
	png := genTestPNG(t)
	var specs []GenSpec
	var mu sync.Mutex
	fakeGenNode(t, c, "n", png, &specs, &mu, func(GenSpec) *ErrInfo { return nil })
	go c.genLoop()
	m := genMux(c)
	do := func(method, path, body string) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		m.ServeHTTP(rec, httptest.NewRequest(method, path, strings.NewReader(body)))
		return rec
	}
	rec := do("POST", "/api/images", `{"model":"flux1-schnell-q8","prompt":"a stone outpost","count":2,"seed":100}`)
	if rec.Code != 201 {
		t.Fatalf("create %d %s", rec.Code, rec.Body)
	}
	var created struct {
		Generation GenRec `json:"generation"`
	}
	json.Unmarshal(rec.Body.Bytes(), &created)
	id := created.Generation.ID
	g := waitGen(t, c, id, "done")
	if len(g.Items) != 2 || g.Items[0].Seed != 100 || g.Items[1].Seed != 101 {
		t.Fatalf("items %+v", g.Items)
	}
	mu.Lock()
	if len(specs) != 2 || specs[0].Width != 1024 || specs[0].Steps != 4 || specs[0].Offload != true || specs[0].Dir != "img-flux1-schnell-q8" || len(specs[0].Files) != 4 {
		t.Fatalf("specs %+v", specs)
	}
	mu.Unlock()
	f := do("GET", "/api/images/"+id+"/files/1", "")
	if f.Code != 200 || f.Header().Get("Content-Type") != "image/png" || !bytes.Equal(f.Body.Bytes(), png) {
		t.Fatalf("file %d %s", f.Code, f.Header().Get("Content-Type"))
	}
	if _, err := os.Stat(filepath.Join(c.genDir(), id, ".fetch-0")); err == nil {
		t.Fatal("temporary download folder was left behind")
	}
	// The node's measured compute buffer is remembered for the next estimate.
	if c.gens.settings.Observed[genObsKey("flux1-schnell-q8", "n", 1024, 1024)] != 700<<20 {
		t.Fatalf("observed %v", c.gens.settings.Observed)
	}
	// The record survives a restart.
	// (the last save lands just after the in-memory status flips, so give it a moment)
	var r *GenRec
	for i := 0; i < 200; i++ {
		c2 := genController(t)
		c2.dataDir = c.dataDir
		c2.loadGens()
		if r = c2.gens.recs[id]; r != nil && r.status() == "done" && r.Items[1].Seconds == 1.5 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if r == nil || r.status() != "done" || r.Items[1].Seconds != 1.5 {
		t.Fatalf("reloaded %+v", r)
	}
	// Delete removes the files.
	if d := do("DELETE", "/api/images/"+id, ""); d.Code != 200 {
		t.Fatalf("delete %d", d.Code)
	}
	if _, err := os.Stat(filepath.Join(c.genDir(), id)); err == nil {
		t.Fatal("the folder should be gone")
	}
}

func TestGenerationRetryAndFailure(t *testing.T) {
	c := genController(t)
	var specs []GenSpec
	var mu sync.Mutex
	calls := 0
	fakeGenNode(t, c, "n", genTestPNG(t), &specs, &mu, func(GenSpec) *ErrInfo {
		mu.Lock()
		defer mu.Unlock()
		calls++
		if calls == 1 {
			return errf("model_missing", true, "model is not on this node")
		}
		if calls >= 2 && calls <= 3 {
			return errf("gen_failed", false, "out of memory")
		}
		return nil
	})
	go c.genLoop()
	rec, err := c.newGen(GenRequest{Model: "flux1-schnell-q8", Prompt: "x", Count: 2}, "web")
	if err != nil {
		t.Fatal(err)
	}
	g := waitGen(t, c, rec.ID, "failed")
	_ = g
	// First call failed retryably and ran again; the later failures are final and show their reason.
	c.gens.mu.Lock()
	msgs := []string{rec.Items[0].Error, rec.Items[1].Error}
	c.gens.mu.Unlock()
	if !strings.Contains(strings.Join(msgs, "|"), "out of memory") {
		t.Fatalf("errors %v", msgs)
	}
	// Retry runs the failed pictures again, now successfully.
	m := genMux(c)
	r := httptest.NewRecorder()
	m.ServeHTTP(r, httptest.NewRequest("POST", "/api/images/"+rec.ID+"/retry", nil))
	waitGen(t, c, rec.ID, "done")
}

func TestGenerationRefusals(t *testing.T) {
	c := genController(t)
	if _, err := c.newGen(GenRequest{Model: "nope", Prompt: "x"}, "web"); err == nil {
		t.Fatal("unknown model")
	}
	if _, err := c.newGen(GenRequest{Model: "flux1-schnell-q8", Prompt: "x", Width: 999}, "web"); err == nil {
		t.Fatal("bad size")
	}
	if _, err := c.newGen(GenRequest{Model: "flux1-schnell-q8", Prompt: "x", Count: 99}, "web"); err == nil {
		t.Fatal("too many")
	}
	// A restricted licence needs an accept click first.
	if _, err := c.newGen(GenRequest{Model: "sdxl-turbo", Prompt: "x"}, "web"); err == nil || !strings.Contains(err.Error(), "licence") {
		t.Fatalf("restricted model: %v", err)
	}
	m := genMux(c)
	m.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("POST", "/api/images/models/sdxl-turbo/accept", nil))
	if _, err := c.newGen(GenRequest{Model: "sdxl-turbo", Prompt: "x"}, "web"); err != nil {
		t.Fatalf("after accepting: %v", err)
	}
	// No node can run it: offline, too small, or too old.
	delete(c.conns, "n")
	if _, err := c.newGen(GenRequest{Model: "flux1-schnell-q8", Prompt: "x"}, "web"); err == nil {
		t.Fatal("offline node")
	}
	c.conns["n"] = &nodeConn{}
	c.nodes["n"].Hello.Features = nil
	if _, err := c.newGen(GenRequest{Model: "flux1-schnell-q8", Prompt: "x"}, "web"); err == nil {
		t.Fatal("old node")
	}
	c.nodes["n"].Hello.Features = []string{"node.gen"}
	c.nodes["n"].Hello.GPUs[0].VRAMBytes = 4 << 30
	if _, err := c.newGen(GenRequest{Model: "flux1-schnell-q8", Prompt: "x"}, "web"); err == nil {
		t.Fatal("4 GB card")
	}
}

func TestPickGenNode(t *testing.T) {
	nodes := []GenNode{
		{ID: "cpu", Name: "cpu", Fit: fitRAM, CanRun: true, HasModel: true, Ready: true},
		{ID: "gpu", Name: "gpu", Fit: fitVRAM, CanRun: true, HasModel: true, Ready: true, VRAM: 16 << 30},
		{ID: "new", Name: "new", Fit: fitVRAM, CanRun: true, VRAM: 24 << 30},
		{ID: "busy", Name: "busy", Fit: fitVRAM, CanRun: true, Busy: true, Ready: true, HasModel: true, VRAM: 32 << 30},
		{ID: "off", Name: "off", Fit: fitVRAM, Ready: true, HasModel: true},
	}
	if n := pickGenNode(nodes); n == nil || n.ID != "gpu" {
		t.Fatalf("got %+v", n)
	}
	nodes[1].Busy = true
	if n := pickGenNode(nodes); n == nil || n.ID != "cpu" {
		t.Fatalf("a node that already has the model beats a faster one that must download it: %+v", n)
	}
	for i := range nodes {
		nodes[i].Busy = true
	}
	if pickGenNode(nodes) != nil {
		t.Fatal("all busy")
	}
}

func TestGenerationRetention(t *testing.T) {
	c := genController(t)
	mk := func(id string, age time.Duration, keep bool, size int) {
		rec := &GenRec{ID: id, Kind: genImage, Created: time.Now().Add(-age), Keep: keep, Items: []*GenItem{{Status: "done"}}}
		c.gens.recs[id] = rec
		c.saveGen(rec)
		os.WriteFile(genFile(filepath.Join(c.genDir(), id), 0), bytes.Repeat([]byte{1}, size), 0o644)
	}
	mk("old", 40*24*time.Hour, false, 10)
	mk("oldkept", 40*24*time.Hour, true, 10)
	mk("new", time.Hour, false, 10)
	running := &GenRec{ID: "running", Created: time.Now().Add(-90 * 24 * time.Hour), Items: []*GenItem{{Status: "running"}}}
	c.gens.recs["running"] = running
	c.saveGen(running)
	c.pruneGens()
	for id, want := range map[string]bool{"old": false, "oldkept": true, "new": true, "running": true} {
		if _, ok := c.gens.recs[id]; ok != want {
			t.Errorf("%s present=%v want %v", id, ok, want)
		}
	}
	if _, err := os.Stat(filepath.Join(c.genDir(), "old")); err == nil {
		t.Fatal("old folder should be deleted")
	}
	// Size cap: 1 GB cap with 1.5 GB used removes the oldest unkept first.
	c.gens.settings.RetentionDays = 0
	c.gens.settings.MaxGB = 1
	big := func(id string, age time.Duration) {
		rec := &GenRec{ID: id, Created: time.Now().Add(-age), Items: []*GenItem{{Status: "done"}}}
		c.gens.recs[id] = rec
		c.saveGen(rec)
		f, _ := os.Create(genFile(filepath.Join(c.genDir(), id), 0))
		f.Truncate(768 << 20) // sparse
		f.Close()
	}
	big("b1", 3*time.Hour)
	big("b2", 2*time.Hour)
	c.pruneGens()
	if _, ok := c.gens.recs["b1"]; ok {
		t.Fatal("the oldest should go first")
	}
	if _, ok := c.gens.recs["b2"]; !ok {
		t.Fatal("the newer one fits under the cap")
	}
}

func TestGenerationCancel(t *testing.T) {
	c := genController(t)
	rec, err := c.newGen(GenRequest{Model: "flux1-schnell-q8", Prompt: "x", Count: 3}, "web")
	if err != nil {
		t.Fatal(err)
	}
	if !c.cancelGen(rec.ID) {
		t.Fatal("cancel")
	}
	c.gens.mu.Lock()
	st := rec.status()
	c.gens.mu.Unlock()
	if st != "cancelled" {
		t.Fatalf("status %s", st)
	}
}
