package main

// Wire protocol v1 — see "Cluster Controller–Node Protocol Spec".

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"time"
)

const ProtocolVersion = 1

// Version is a constant so buildInfo lands in the binary as one literal the controller
// can find when you upload a release.
const Version = "0.7.3"

const buildInfo = "OUTPOST_BUILD_INFO|" + Version + "|"

type Envelope struct {
	V       int             `json:"v"`
	Type    string          `json:"type"`
	ID      string          `json:"id"`
	ReplyTo string          `json:"reply_to,omitempty"`
	TS      time.Time       `json:"ts"`
	Body    json.RawMessage `json:"body"`
}

func newEnvelope(typ string, body any) ([]byte, error) {
	b, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}
	return json.Marshal(Envelope{V: ProtocolVersion, Type: typ, ID: newID("msg"), TS: time.Now().UTC(), Body: b})
}

// newID returns prefix_ + 13 hex chars of millisecond time + 12 random hex chars,
// so IDs sort roughly by creation time (ULID-like).
func newID(prefix string) string {
	r := make([]byte, 6)
	_, _ = rand.Read(r)
	return fmt.Sprintf("%s_%013x%s", prefix, time.Now().UnixMilli(), hex.EncodeToString(r))
}

func randHex(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

type ErrInfo struct {
	Code      string `json:"code"`
	Message   string `json:"message"`
	Retryable bool   `json:"retryable"`
}

func (e *ErrInfo) Error() string { return e.Code + ": " + e.Message }

func errf(code string, retry bool, format string, a ...any) *ErrInfo {
	return &ErrInfo{Code: code, Message: fmt.Sprintf(format, a...), Retryable: retry}
}

// ---- registration ----

type CPUInfo struct {
	Model   string `json:"model"`
	Sockets int    `json:"sockets"`
	Cores   int    `json:"cores"`
	Threads int    `json:"threads"`
}

type GPUInfo struct {
	Vendor    string `json:"vendor"`
	Name      string `json:"name"`
	VRAMBytes uint64 `json:"vram_bytes"`
	Backend   string `json:"backend"`
}

type ModelInfo struct {
	Name  string `json:"name"`
	Bytes int64  `json:"bytes"`
}

type Hello struct {
	AgentVersion      string       `json:"agent_version"`
	Protocol          []int        `json:"protocol"`
	NodeID            string       `json:"node_id"`
	Hostname          string       `json:"hostname"`
	OS                string       `json:"os"`
	Arch              string       `json:"arch"`
	CPU               CPUInfo      `json:"cpu"`
	RAMBytes          uint64       `json:"ram_bytes"`
	GPUs              []GPUInfo    `json:"gpus"`
	Backends          []string     `json:"backends"`
	Models            []ModelInfo  `json:"models"`
	WorkFreeBytes     uint64       `json:"work_free_bytes"`
	ModelsFreeBytes   uint64       `json:"models_free_bytes"`
	MaxConcurrentJobs int          `json:"max_concurrent_jobs"`
	ExecEnabled       bool         `json:"exec_enabled"`
	RunningJobs       []string     `json:"running_jobs"`
	Runtime           *RuntimeInfo `json:"runtime,omitempty"`
	VulkanAvailable   bool         `json:"vulkan_available"`
	LoadedModel       string       `json:"loaded_model"`
	RollbackTo        string       `json:"rollback_to,omitempty"` // previous installed Outpost version
	Paused            bool         `json:"paused,omitempty"`
}

// NodeRollback asks a node to restart into an older installed version.
type NodeRollback struct {
	Version string `json:"version"`
}

type NodeSettings struct {
	HeartbeatS  int  `json:"heartbeat_s"`
	ExecEnabled bool `json:"exec_enabled"`
}

type Welcome struct {
	Accepted bool         `json:"accepted"`
	Protocol int          `json:"protocol"`
	Error    *ErrInfo     `json:"error,omitempty"`
	Settings NodeSettings `json:"settings"`
}

type Heartbeat struct {
	CPUPct        float64  `json:"cpu_pct"`
	RAMUsedBytes  uint64   `json:"ram_used_bytes"`
	RAMTotalBytes uint64   `json:"ram_total_bytes"`
	SwapUsedBytes uint64   `json:"swap_used_bytes"`
	GPUUtilPct    []int    `json:"gpu_util_pct"`
	VRAMUsedBytes []uint64 `json:"vram_used_bytes"`
	RunningJobs   []string `json:"running_jobs"`
	Workspaces    int      `json:"workspaces"`
	LoadedModel   string   `json:"loaded_model"`
	InferenceRdy  bool     `json:"inference_ready"`
	InferenceBusy int      `json:"inference_busy"`
	ModelsBytes   int64    `json:"models_bytes"`
	LlamaError    string   `json:"llama_error,omitempty"`
	RPCEndpoint   string   `json:"rpc_endpoint,omitempty"` // set while lending memory
	Paused        bool     `json:"paused,omitempty"`       // no new work (set from the node's tray icon)
}

// ---- jobs ----

type FileEntry struct {
	Path      string `json:"path"`
	SHA256    string `json:"sha256"`
	Bytes     int64  `json:"bytes"`
	Mode      string `json:"mode,omitempty"`
	InlineB64 string `json:"inline_b64,omitempty"`
	Blob      bool   `json:"blob,omitempty"`
	Extract   bool   `json:"extract,omitempty"`
}

type JobDispatch struct {
	JobID        string          `json:"job_id"`
	ParentTaskID string          `json:"parent_task_id,omitempty"`
	WorkspaceID  string          `json:"workspace_id,omitempty"`
	Kind         string          `json:"kind"`
	TimeoutS     int             `json:"timeout_s"`
	Inputs       []FileEntry     `json:"inputs"`
	Collect      []string        `json:"collect"`
	Spec         json.RawMessage `json:"spec"`
}

type ExecSpec struct {
	Argv   []string          `json:"argv"`
	Env    map[string]string `json:"env,omitempty"`
	Stream bool              `json:"stream"`
}

type ExecOutput struct {
	ExitCode   int    `json:"exit_code"`
	StdoutTail string `json:"stdout_tail"`
	StderrTail string `json:"stderr_tail"`
}

type JobOutput struct {
	JobID  string `json:"job_id"`
	Seq    int    `json:"seq"`
	Stream string `json:"stream"`
	Data   string `json:"data"`
	Tokens int    `json:"tokens,omitempty"` // model tokens in Data (inference jobs)
}

type JobResult struct {
	JobID     string          `json:"job_id"`
	Status    string          `json:"status"` // succeeded | failed | cancelled | timed_out
	Error     *ErrInfo        `json:"error"`
	Output    json.RawMessage `json:"output"`
	Files     []FileEntry     `json:"files"`
	StartedAt time.Time       `json:"started_at"`
	EndedAt   time.Time       `json:"ended_at"`
}

type JobCancel struct {
	JobID  string `json:"job_id"`
	Reason string `json:"reason"`
}

type WSClose struct {
	WorkspaceID string `json:"workspace_id"`
}

type WSClosed struct {
	WorkspaceID string `json:"workspace_id"`
	Reason      string `json:"reason"`
	BytesFreed  int64  `json:"bytes_freed"`
}

// ---- join ----

type JoinToken struct {
	URL         string `json:"u"`
	Fingerprint string `json:"f"`
	Secret      string `json:"s"`
}

type JoinRequest struct {
	Secret   string `json:"secret"`
	Hostname string `json:"hostname"`
}

type JoinResponse struct {
	NodeID  string `json:"node_id"`
	NodeKey string `json:"node_key"`
}

// ---- self-update ----

// NodeUpdate asks a node to install a release from GET /v1/releases/<platform>.
type NodeUpdate struct {
	Version  string `json:"version"`
	Platform string `json:"platform"`
	SHA256   string `json:"sha256"`
	Bytes    int64  `json:"bytes"`
}

// UpdateStatus reports progress: downloading | installing | restarting | failed.
type UpdateStatus struct {
	Version    string    `json:"version"`
	State      string    `json:"state"`
	Error      string    `json:"error,omitempty"`
	BytesDone  int64     `json:"bytes_done,omitempty"`
	BytesTotal int64     `json:"bytes_total,omitempty"`
	Updated    time.Time `json:"updated"`
}

// ---- models and llama.cpp runtime ----

// RuntimeInfo identifies an installed llama.cpp build on a node.
type RuntimeInfo struct {
	Build   string `json:"build"`   // e.g. b11669
	Variant string `json:"variant"` // e.g. linux-x64-vulkan
}

// RuntimeInstall asks a node to install a llama.cpp build from GET /v1/runtime/<variant>.
type RuntimeInstall struct {
	Build   string `json:"build"`
	Variant string `json:"variant"`
	Archive string `json:"archive"` // file name, decides tar.gz vs zip
	SHA256  string `json:"sha256"`
	Bytes   int64  `json:"bytes"`
}

type ModelFile struct {
	Path   string `json:"path"` // file name inside the model directory
	SHA256 string `json:"sha256"`
	Bytes  int64  `json:"bytes"`
}

// ModelPush asks a node to fetch a model from GET /v1/models/<name>/files/<index>.
type ModelPush struct {
	Name  string      `json:"name"`
	Files []ModelFile `json:"files"`
}

// ModelLoad starts llama-server with a model. Zero values mean llama.cpp defaults.
// ModelFetch asks a node to download a model straight from Hugging Face.
type ModelFetch struct {
	Name  string           `json:"name"`
	Files []ModelFetchFile `json:"files"`
	Token string           `json:"token,omitempty"` // Hugging Face token, only for gated models
}

type ModelFetchFile struct {
	ModelFile
	URL string `json:"url"`
}

type ModelLoad struct {
	Name      string   `json:"name"`
	Ctx       int      `json:"ctx"`
	Parallel  int      `json:"parallel"`
	Threads   int      `json:"threads"`
	GPULayers string   `json:"gpu_layers"` // "", "auto", "all" or a number
	ExtraArgs []string `json:"extra_args"`
	RPC       []string `json:"rpc,omitempty"` // helper nodes' RPC servers (host:port) to pool memory from
}

// RPCStart asks a node to lend its memory: run llama.cpp's RPC server.
type RPCStart struct {
	Host    string   `json:"host"`  // address the node is reached at on the LAN
	Port    int      `json:"port"`  // 0 = choose
	Allow   []string `json:"allow"` // only these IPs (the main node) may connect
	Threads int      `json:"threads"`
}

type ModelRemove struct {
	Name string `json:"name"`
}

// OpStatus reports progress of a long node operation (runtime, model push, load).
type OpStatus struct {
	Op         string  `json:"op"`     // runtime | model.push | model.load | model.unload | model.remove
	Target     string  `json:"target"` // model name or runtime variant
	State      string  `json:"state"`  // downloading | verifying | installing | loading | ready | done | failed
	Progress   float64 `json:"progress"`
	BytesDone  int64   `json:"bytes_done"`
	BytesTotal int64   `json:"bytes_total"`
	Error      string  `json:"error,omitempty"`
	Detail     string  `json:"detail,omitempty"` // e.g. the RPC endpoint once ready
}

// InferenceSpec is the spec of a job with kind "inference".
type InferenceSpec struct {
	Model    string          `json:"model,omitempty"`
	Messages []ChatMessage   `json:"messages"`
	Params   InferenceParams `json:"params"`
	Stream   bool            `json:"stream"`
}

type ChatMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type InferenceParams struct {
	Temperature *float64 `json:"temperature,omitempty"`
	MaxTokens   int      `json:"max_tokens,omitempty"`
	Stop        []string `json:"stop,omitempty"`
}

type InferenceOutput struct {
	Text         string  `json:"text"`
	FinishReason string  `json:"finish_reason"`
	PromptTokens int     `json:"prompt_tokens"`
	OutputTokens int     `json:"output_tokens"`
	TokensPerSec float64 `json:"tokens_per_s"`
	Model        string  `json:"model"`
}
