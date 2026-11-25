package main

import (
	"context"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"math"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"
)

// fakeEmbedNode answers "embed" jobs on node id with a vector per text: [len(text), 1, 0] (so it is easy to check),
// and records every batch it was sent.
func fakeEmbedNode(t *testing.T, c *Controller, id string, batches *[][]string, mu *sync.Mutex) {
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
			var spec EmbedSpec
			json.Unmarshal(jd.Spec, &spec)
			mu.Lock()
			*batches = append(*batches, spec.Texts)
			mu.Unlock()
			out := EmbedOutput{Model: spec.Model, Dim: 3, Tokens: len(spec.Texts)}
			for _, s := range spec.Texts {
				out.Vectors = append(out.Vectors, []float32{float32(len(s)), 1, 0})
			}
			ob, _ := json.Marshal(out)
			res, _ := json.Marshal(JobResult{JobID: jd.JobID, Status: "succeeded", Output: ob})
			c.handleNodeMessage(id, &Envelope{Type: "job.result", Body: res})
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

func embedController(t *testing.T) (*Controller, *[][]string, *sync.Mutex) {
	c := chatController(t)
	c.nodes["n"].Hello = &Hello{Runtime: &RuntimeInfo{Build: "b1"}, Models: []ModelInfo{{Name: "nomic-embed-text-v1.5-q8", Bytes: 1}}, CPU: CPUInfo{Threads: 4}}
	c.models["nomic-embed-text-v1.5-q8"] = &ModelRec{Name: "nomic-embed-text-v1.5-q8", Status: "ready", Embedding: true}
	c.models["chatty"] = &ModelRec{Name: "chatty", Status: "ready"}
	var batches [][]string
	var mu sync.Mutex
	fakeEmbedNode(t, c, "n", &batches, &mu)
	return c, &batches, &mu
}

func TestEmbeddingsEndpoint(t *testing.T) {
	c, batches, mu := embedController(t)
	m := http.NewServeMux()
	m.HandleFunc("POST /v1/embeddings", c.handleV1Embeddings)
	m.HandleFunc("GET /v1/models", c.handleV1Models)
	c.openAIRoutes(http.NewServeMux()) // the real routes register without clashing
	do := func(path, body string) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		method := "POST"
		if body == "" {
			method = "GET"
		}
		m.ServeHTTP(rec, httptest.NewRequest(method, path, strings.NewReader(body)))
		return rec
	}

	// One string and a list; vectors come back unit length, in order.
	rec := do("/v1/embeddings", `{"model":"auto","input":["ab","abcd"]}`)
	if rec.Code != 200 {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	var out struct {
		Data []struct {
			Index     int       `json:"index"`
			Embedding []float64 `json:"embedding"`
		}
		Model string
		Usage struct {
			Prompt int `json:"prompt_tokens"`
		}
	}
	json.Unmarshal(rec.Body.Bytes(), &out)
	if len(out.Data) != 2 || out.Model != "nomic-embed-text-v1.5-q8" || out.Usage.Prompt != 2 {
		t.Fatalf("answer %s", rec.Body)
	}
	for i, d := range out.Data {
		var n float64
		for _, x := range d.Embedding {
			n += x * x
		}
		if d.Index != i || math.Abs(n-1) > 1e-5 {
			t.Fatalf("vector %d not unit length: %v", i, d)
		}
	}
	if out.Data[0].Embedding[0] >= out.Data[1].Embedding[0] { // [2,1,0] vs [4,1,0] normalised: the longer text leans more to x
		t.Fatalf("order lost: %v", out.Data)
	}
	if rec := do("/v1/embeddings", `{"input":"hello"}`); rec.Code != 200 {
		t.Fatalf("single string %d %s", rec.Code, rec.Body)
	}

	// base64, as the OpenAI Python client asks for by default.
	rec = do("/v1/embeddings", `{"input":"abcd","encoding_format":"base64"}`)
	var b64 struct {
		Data []struct{ Embedding string }
	}
	json.Unmarshal(rec.Body.Bytes(), &b64)
	raw, err := base64.StdEncoding.DecodeString(b64.Data[0].Embedding)
	if err != nil || len(raw) != 12 {
		t.Fatalf("base64 %v %d", err, len(raw))
	}
	x := math.Float32frombits(binary.LittleEndian.Uint32(raw))
	if x < 0.9 || x > 1 {
		t.Fatalf("first component %v", x)
	}

	// Refusals.
	for body, want := range map[string]int{
		`{"input":""}`:                        400,
		`{"input":[]}`:                        400,
		`{}`:                                  400,
		`{"input":[[1,2,3]]}`:                 400,
		`{"input":"x","dimensions":64}`:       400,
		`{"input":"x","encoding_format":"q"}`: 400,
		`{"model":"chatty","input":"x"}`:      404,
		`{"model":"nope","input":"x"}`:        404,
	} {
		if rec := do("/v1/embeddings", body); rec.Code != want {
			t.Errorf("%s: %d %s, want %d", body, rec.Code, rec.Body, want)
		}
	}
	mu.Lock()
	defer mu.Unlock()
	if len(*batches) != 3 { // the two good calls and the base64 one
		t.Errorf("node saw %d batches: %v", len(*batches), *batches)
	}

	// /v1/models lists it as an embedding model, not as a chat model.
	rec = do("/v1/models", "")
	if !strings.Contains(rec.Body.String(), `"id":"nomic-embed-text-v1.5-q8"`) || !strings.Contains(rec.Body.String(), `"capabilities":["embedding"]`) {
		t.Errorf("models: %s", rec.Body)
	}
}

func TestEmbedBatchesAndPrefix(t *testing.T) {
	c, batches, mu := embedController(t)
	texts := make([]string, 70)
	for i := range texts {
		texts[i] = "t"
	}
	res, err := c.Embed(context.Background(), "", "query", texts)
	if err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(res.Vectors) != 70 || len(*batches) != 3 || len((*batches)[0]) != 32 || len((*batches)[2]) != 6 {
		t.Fatalf("70 texts should go as 32+32+6, got %d batches", len(*batches))
	}
	if (*batches)[0][0] != "search_query: t" {
		t.Fatalf("nomic wants a query prefix: %q", (*batches)[0][0])
	}
	if embedPrefix("nomic-embed-text-v1.5-q8", "document") != "search_document: " || embedPrefix("nomic-embed-text-v1.5-q8", "") != "" || embedPrefix("llama", "query") != "" {
		t.Error("prefixes")
	}
	if _, err := c.Embed(context.Background(), "", "", []string{strings.Repeat("x", embedMaxChars+1)}); err == nil || !strings.Contains(err.Error(), "too long") {
		t.Errorf("long text: %v", err)
	}
}

func TestEmbedNeedsAModelAndNode(t *testing.T) {
	c := chatController(t)
	if _, err := c.Embed(context.Background(), "", "", []string{"x"}); err == nil || !strings.Contains(err.Error(), "no embedding model") {
		t.Errorf("no model: %v", err)
	}
	c.models["nomic-embed-text-v1.5-q8"] = &ModelRec{Name: "nomic-embed-text-v1.5-q8", Status: "ready"}
	if _, err := c.Embed(context.Background(), "", "", []string{"x"}); err == nil || !strings.Contains(err.Error(), "no online node") {
		t.Errorf("no node: %v", err)
	}
}

func TestParseEmbeddingsAndNames(t *testing.T) {
	v, tok, err := parseEmbeddings([]byte(`{"data":[{"index":1,"embedding":[0,1]},{"index":0,"embedding":[1,0]}],"usage":{"prompt_tokens":7}}`), 2)
	if err != nil || tok != 7 || v[0][0] != 1 || v[1][1] != 1 {
		t.Fatalf("%v %d %v", err, tok, v)
	}
	for _, bad := range []string{`{"data":[]}`, `{"data":[{"index":5,"embedding":[1]}]}`, `{"data":[{"index":0,"embedding":[1]},{"index":1,"embedding":[1,2]}]}`, `nope`} {
		if _, _, err := parseEmbeddings([]byte(bad), 2); err == nil {
			t.Errorf("%s should fail", bad)
		}
	}
	for name, want := range map[string]bool{"nomic-embed-text-v1.5-q8": true, "qwen3-embedding-0.6b-q8": true, "bge-small-en-v1.5": true, "my-Embed-model": true,
		"qwen2.5-coder-7b-q4km": false, "qwen3.6-27b-q4km": false} {
		if isEmbeddingName(name) != want {
			t.Errorf("isEmbeddingName(%q) != %v", name, want)
		}
	}
	if ce := catalogEntry("nomic-embed-text-v1.5-q8"); ce == nil || !ce.Embedding {
		t.Error("catalog entry")
	}
}
