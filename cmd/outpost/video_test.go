package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

func TestVideoSpecAndFit(t *testing.T) {
	d := imageModel("wan21-t2v-1.3b")
	if d == nil || d.Kind != genVideo || d.DefaultHeight() != 480 {
		t.Fatalf("catalog entry %+v", d)
	}
	ok := GenSpec{Kind: genVideo, Prompt: "x", Width: 832, Height: 480, Steps: 20, CFG: 6, Frames: 33, FPS: 16, Files: []GenArg{{Flag: "--vae", File: "a"}}}
	if err := ok.validate(); err != nil {
		t.Fatal(err)
	}
	for name, mut := range map[string]func(*GenSpec){
		"few frames": func(s *GenSpec) { s.Frames = 2 },
		"many":       func(s *GenSpec) { s.Frames = 500 },
		"fps":        func(s *GenSpec) { s.FPS = 0 },
		"wide":       func(s *GenSpec) { s.Width = 1920 },
	} {
		s := ok
		mut(&s)
		if s.validate() == nil {
			t.Errorf("%s should be refused", name)
		}
	}
	// a longer clip needs more memory
	short, _ := estimateGen(d, 832, 480, 17, 0)
	long, _ := estimateGen(d, 832, 480, 81, 0)
	if long <= short {
		t.Fatalf("81 frames (%d) should need more than 17 (%d)", long, short)
	}
	if fit, _ := genFit(d, 832, 480, 33, 0, 16<<30, 32<<30); fit != fitVRAM && fit != fitSplit {
		t.Fatalf("fit on 16 GB: %s", fit)
	}
	if fit, _ := genFit(d, 832, 480, 33, 0, 0, 2<<30); fit != fitNo {
		t.Fatalf("fit on 2 GB of RAM: %s", fit)
	}
	body := sdRequestBody(ok)
	if body["video_frames"] != 33 || body["fps"] != 16 || body["output_format"] != "webm" {
		t.Fatalf("body %v", body)
	}
	args := strings.Join(sdServerArgs(ok, "/m", 1234), " ")
	if !strings.Contains(args, "-M vid_gen") {
		t.Fatalf("args %s", args)
	}
	t.Setenv("OUTPOST_SD_VIDEO_ARGS", "none")
	if strings.Contains(strings.Join(sdServerArgs(ok, "/m", 1234), " "), "vid_gen") {
		t.Fatal("override ignored")
	}
}

// fakeVidSD answers vid_gen; refuseWebm makes it refuse the webm container once.
func fakeVidSD(t *testing.T, clip []byte, refuseWebm bool, formats chan<- string) *httptest.Server {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/sdcpp/v1/vid_gen":
			var body map[string]any
			json.NewDecoder(r.Body).Decode(&body)
			f, _ := body["output_format"].(string)
			if formats != nil {
				formats <- f
			}
			if refuseWebm && f == "webm" {
				w.WriteHeader(400)
				io.WriteString(w, `{"error":"unsupported output format"}`)
				return
			}
			w.WriteHeader(202)
			io.WriteString(w, `{"id":"job_v","kind":"vid_gen","status":"queued"}`)
		case r.URL.Path == "/sdcpp/v1/jobs/job_v":
			b, _ := json.Marshal(map[string]any{"id": "job_v", "status": "completed", "result": map[string]any{
				"output_format": "avi", "frame_count": 33, "b64_json": base64.StdEncoding.EncodeToString(clip)}})
			w.Write(b)
		default:
			w.WriteHeader(404)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestSDVideoAgainstFakeServer(t *testing.T) {
	clip := []byte("RIFF....AVI fake clip")
	formats := make(chan string, 4)
	srv := fakeVidSD(t, clip, true, formats)
	n := &Node{}
	n.sd = &sdMgr{n: n, testURL: srv.URL}
	spec := GenSpec{Kind: genVideo, Prompt: "waves", Width: 832, Height: 480, Steps: 20, CFG: 6, Frames: 33, FPS: 16, Seed: 7}
	got, ext, e := n.sd.generateExt(context.Background(), "j", n, spec)
	if e != nil {
		t.Fatal(e)
	}
	if !bytes.Equal(got, clip) || ext != "avi" {
		t.Fatalf("got %q ext %s", got, ext)
	}
	if a, b := <-formats, <-formats; a != "webm" || b != "avi" {
		t.Fatalf("formats tried: %s then %s", a, b)
	}
}

func TestVideoEndToEnd(t *testing.T) {
	c := genController(t)
	clip := genTestPNG(t) // any bytes stand in for the clip
	var specs []GenSpec
	var mu sync.Mutex
	fakeGenNode(t, c, "n", clip, &specs, &mu, func(GenSpec) *ErrInfo { return nil })
	go c.genLoop()
	m := genMux(c)
	do := func(method, path, body string) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		m.ServeHTTP(rec, httptest.NewRequest(method, path, strings.NewReader(body)))
		return rec
	}
	if r := do("POST", "/api/images", `{"model":"wan21-t2v-1.3b","prompt":"x","frames":500}`); r.Code != 400 {
		t.Fatalf("too many frames: %d", r.Code)
	}
	if r := do("POST", "/api/images", `{"model":"wan21-t2v-1.3b","prompt":"x","count":9}`); r.Code != 400 {
		t.Fatalf("too many clips: %d", r.Code)
	}
	rec := do("POST", "/api/images", `{"model":"wan21-t2v-1.3b","prompt":"waves on a beach","frames":17}`)
	if rec.Code != 201 {
		t.Fatalf("create %d %s", rec.Code, rec.Body)
	}
	var created struct {
		Generation GenRec `json:"generation"`
	}
	json.Unmarshal(rec.Body.Bytes(), &created)
	g := waitGen(t, c, created.Generation.ID, "done")
	if g.Kind != genVideo || g.Frames != 17 || g.FPS != 16 || g.Width != 832 || g.Height != 480 || g.Items[0].Ext != "webm" {
		t.Fatalf("record %+v item %+v", g, g.Items[0])
	}
	mu.Lock()
	if len(specs) != 1 || specs[0].Kind != genVideo || specs[0].Frames != 17 || specs[0].FPS != 16 {
		t.Fatalf("specs %+v", specs)
	}
	mu.Unlock()
	f := do("GET", "/api/images/"+g.ID+"/files/0?download=1", "")
	if f.Code != 200 || f.Header().Get("Content-Type") != "video/webm" || !bytes.Equal(f.Body.Bytes(), clip) ||
		!strings.HasSuffix(f.Header().Get("Content-Disposition"), `.webm"`) {
		t.Fatalf("file %d %s %s", f.Code, f.Header().Get("Content-Type"), f.Header().Get("Content-Disposition"))
	}
	// an image model keeps working and a clip never reaches the picture API
	c.gens.mu.Lock()
	c.gens.settings.APIEnabled = true
	c.gens.mu.Unlock()
	mux := http.NewServeMux()
	c.openAIImageRoutes(mux)
	c.mu.Lock()
	c.apiTokens = append(c.apiTokens, apiToken{ID: "t", Name: "t", Hash: hashAPIToken("tok")})
	c.mu.Unlock()
	r := httptest.NewRequest("POST", "/v1/images/generations", strings.NewReader(`{"model":"wan21-t2v-1.3b","prompt":"x"}`))
	r.Header.Set("Authorization", "Bearer tok")
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, r)
	if w.Code != 400 || !strings.Contains(w.Body.String(), "video") {
		t.Fatalf("video through the picture API: %d %s", w.Code, w.Body)
	}
}
