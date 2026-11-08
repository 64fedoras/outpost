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

// MinProtocolVersion is the oldest protocol this build still speaks. Nodes advertise every
// version from MinProtocolVersion to ProtocolVersion and the controller picks the highest it
// shares, so a protocol bump can overlap one release instead of locking out older nodes.
const MinProtocolVersion = 1

// supportedProtocols lists the protocol versions this build speaks, oldest first.
func supportedProtocols() []int {
	var v []int
	for i := MinProtocolVersion; i <= ProtocolVersion; i++ {
		v = append(v, i)
	}
	return v
}

// negotiateProtocol returns the highest protocol both sides speak.
func negotiateProtocol(theirs []int) (int, bool) {
	best, ok := 0, false
	for _, v := range theirs {
		if v >= MinProtocolVersion && v <= ProtocolVersion && v > best {
			best, ok = v, true
		}
	}
	return best, ok
}

// Version is a constant so buildInfo lands in the binary as one literal the controller
// can find when you upload a release.
const Version = "0.8.0"

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
	MemBWGBs          float64      `json:"mem_bw_gbs,omitempty"` // measured memory read bandwidth, GB/s (0 = not measured yet)
	GPUs              []GPUInfo    `json:"gpus"`
	Backends          []string     `json:"backends"`
	Models            []ModelInfo  `json:"models"`
	WorkFreeBytes     uint64       `json:"work_free_bytes"`
	ModelsFreeBytes   uint64       `json:"models_free_bytes"`
	MaxConcurrentJobs int          `json:"max_concurrent_jobs"`
	ExecEnabled       bool         `json:"exec_enabled"`
	RunningJobs       []string     `json:"running_jobs"`
	Runtime           *RuntimeInfo `json:"runtime,omitempty"`
	SDRuntime         *RuntimeInfo `json:"sd_runtime,omitempty"` // stable-diffusion.cpp build, when installed (image generation)
	VulkanAvailable   bool         `json:"vulkan_available"`
	LoadedModel       string       `json:"loaded_model"`
	RollbackTo        string       `json:"rollback_to,omitempty"` // previous installed Outpost version
	Paused            bool         `json:"paused,omitempty"`
	Features          []string     `json:"features,omitempty"`       // newer messages this node understands (nodeFeatures)
	Tools             []ToolInfo   `json:"tools,omitempty"`          // developer tools found, rescanned hourly
	Shell             string       `json:"shell,omitempty"`          // sh | powershell | cmd: what exec jobs run in
	Peer              *PeerInfo    `json:"peer,omitempty"`           // where this node serves model files to other nodes
	Sandbox           string       `json:"sandbox,omitempty"`        // mode exec jobs run in on this node (empty on nodes older than the sandbox)
	SandboxLayers     []string     `json:"sandbox_layers,omitempty"` // what that mode enforces here, e.g. landlock-v3, netns, seccomp
	FreedNote         string       `json:"freed_note,omitempty"`     // last automatic disk cleanup, e.g. "Freed 12.0 GB by removing X"
	FreedAt           int64        `json:"freed_at,omitempty"`       // unix seconds of that cleanup
	MACs              []string     `json:"macs,omitempty"`           // wired/wireless interfaces with an IPv4 address, for Wake-on-LAN
}

// PeerInfo is a node's model-sharing endpoint: the port and the fingerprint
// of its own self-signed certificate. The controller hands both to nodes that
// want to download from it, so they can pin the certificate.
type PeerInfo struct {
	Port        int    `json:"port"`
	Fingerprint string `json:"fingerprint"`
}

// PeerSource is one node a model can be copied from.
type PeerSource struct {
	Name        string `json:"name"`
	Addr        string `json:"addr"` // host:port
	Fingerprint string `json:"fingerprint"`
}

// PeerGrant lets the holder of Token download from this node until TTLSeconds
// pass. TTLSeconds <= 0 revokes the token.
type PeerGrant struct {
	Token      string `json:"token"`
	TTLSeconds int    `json:"ttl_s"`
}

// NodeRollback asks a node to restart into an older installed version.
type NodeRollback struct {
	Version string `json:"version"`
}

type NodeSettings struct {
	HeartbeatS  int    `json:"heartbeat_s"`
	ExecEnabled bool   `json:"exec_enabled"`
	IdleUnloadS int    `json:"idle_unload_s"`       // unload the model after this many idle seconds; 0 = never
	YieldGPU    bool   `json:"yield_gpu,omitempty"` // unload while another program uses the GPU (gpu_yield.go)
	Sandbox     string `json:"sandbox,omitempty"`   // off | basic | strict; empty = the node's default (basic)
}

type Welcome struct {
	Accepted bool         `json:"accepted"`
	Protocol int          `json:"protocol"`
	Error    *ErrInfo     `json:"error,omitempty"`
	Settings NodeSettings `json:"settings"`
}

type Heartbeat struct {
	CPUPct        float64    `json:"cpu_pct"`
	RAMUsedBytes  uint64     `json:"ram_used_bytes"`
	RAMTotalBytes uint64     `json:"ram_total_bytes"`
	SwapUsedBytes uint64     `json:"swap_used_bytes"`
	GPUUtilPct    []int      `json:"gpu_util_pct"`
	VRAMUsedBytes []uint64   `json:"vram_used_bytes"`
	RunningJobs   []string   `json:"running_jobs"`
	Workspaces    int        `json:"workspaces"`
	LoadedModel   string     `json:"loaded_model"`
	InferenceRdy  bool       `json:"inference_ready"`
	InferenceBusy int        `json:"inference_busy"`
	ModelsBytes   int64      `json:"models_bytes"`
	LlamaError    string     `json:"llama_error,omitempty"`
	RPCEndpoint   string     `json:"rpc_endpoint,omitempty"`  // set while lending memory
	Paused        bool       `json:"paused,omitempty"`        // no new work (set from the tray icon or the web UI)
	PausedBy      string     `json:"paused_by,omitempty"`     // tray | web
	PausedUntil   *time.Time `json:"paused_until,omitempty"`  // a timed pause ends on its own then
	GPUYielding   bool       `json:"gpu_yielding,omitempty"`  // the model is unloaded because another program is using the GPU
	IdleUnloaded  bool       `json:"idle_unloaded,omitempty"` // LoadedModel was stopped for idleness; the next request loads it again
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
	Argv    []string          `json:"argv"`
	Env     map[string]string `json:"env,omitempty"`
	Stream  bool              `json:"stream"`
	Network string            `json:"network,omitempty"` // "allow" opens the network for this command in a strict sandbox; "" = the mode's default
}

type ExecOutput struct {
	ExitCode      int    `json:"exit_code"`
	StdoutTail    string `json:"stdout_tail"`
	StderrTail    string `json:"stderr_tail"`
	Sandbox       string `json:"sandbox,omitempty"`        // mode the command ran in (omitted when off)
	SandboxDenied bool   `json:"sandbox_denied,omitempty"` // the failure looks like the sandbox blocked something
	SandboxNote   string `json:"sandbox_note,omitempty"`   // one line for the fixer about what the sandbox allowed
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
	Runtime string `json:"runtime,omitempty"` // "" = llama.cpp, "sd" = stable-diffusion.cpp (image and video generation)
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
	// Peers, when set, replace the controller as the source: the node copies
	// the files from them and checks each against File.SHA256. If that fails the
	// controller falls back to its normal route.
	Peers     []PeerSource `json:"peers,omitempty"`
	PeerToken string       `json:"peer_token,omitempty"`
	Reclaim   *Reclaim     `json:"reclaim,omitempty"`
}

// Reclaim lets a node free disk space for an incoming model by deleting stored
// models the controller can copy back. Nil or Enabled false: never delete.
type Reclaim struct {
	Enabled bool     `json:"enabled"`
	Names   []string `json:"names"`            // tier 1: stored models the controller holds a ready copy of
	Direct  []string `json:"direct,omitempty"` // tier 2: stored models the node can re-download from Hugging Face; used only after tier 1
}

// ModelLoad starts llama-server with a model. Zero values mean llama.cpp defaults.
// ModelFetch asks a node to download a model straight from Hugging Face.
type ModelFetch struct {
	Name    string           `json:"name"`
	Files   []ModelFetchFile `json:"files"`
	Token   string           `json:"token,omitempty"` // Hugging Face token, only for gated models
	Reclaim *Reclaim         `json:"reclaim,omitempty"`
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
	RPC       []string `json:"rpc,omitempty"`    // helper nodes' RPC servers (host:port) to pool memory from
	MMProj    string   `json:"mmproj,omitempty"` // set by the node: vision projector file of the model, passed as --mmproj
	// CacheType quantises the KV cache: "" (16-bit), "q8_0" or "q4_0".
	CacheType string `json:"cache_type,omitempty"`
	// FlashAttn is "" (llama.cpp decides), "on" or "off".
	FlashAttn string `json:"flash_attn,omitempty"`
	// Draft is a small library model of the same family used for speculative
	// decoding (-md); the node fetches it like the main model.
	Draft string `json:"draft,omitempty"`
	// CPUMoe keeps mixture-of-experts weights in system RAM: "" (off), "all"
	// (--cpu-moe) or a layer count N (--n-cpu-moe N: the experts of the first N layers).
	CPUMoe    string `json:"cpu_moe,omitempty"`
	DraftPath string `json:"draft_path,omitempty"` // set by the node
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

// ModelCleanup asks a node to delete stored models. The node keeps any model
// that is loaded, set to reload, or being copied, whatever the list says.
type ModelCleanup struct {
	Names      []string `json:"names"`
	Incomplete bool     `json:"incomplete,omitempty"` // also delete folders left by interrupted downloads
}

// OpStatus reports progress of a long node operation (runtime, model push, load).
type OpStatus struct {
	Op         string   `json:"op"`     // runtime | model.push | model.load | model.unload | model.remove
	Target     string   `json:"target"` // model name or runtime variant
	State      string   `json:"state"`  // downloading | verifying | installing | loading | ready | done | failed
	Progress   float64  `json:"progress"`
	BytesDone  int64    `json:"bytes_done"`
	BytesTotal int64    `json:"bytes_total"`
	Error      string   `json:"error,omitempty"`
	Detail     string   `json:"detail,omitempty"`    // e.g. the RPC endpoint once ready
	BadPeers   []string `json:"bad_peers,omitempty"` // nodes that served data failing its hash check
}

// InferenceSpec is the spec of a job with kind "inference".
type InferenceSpec struct {
	Model    string          `json:"model,omitempty"`
	Messages []ChatMessage   `json:"messages"`
	Params   InferenceParams `json:"params"`
	Stream   bool            `json:"stream"`
	Tools    []ToolDef       `json:"tools,omitempty"` // agent mode: tools the model may call
}

type ChatMessage struct {
	Role       string         `json:"role"`
	Content    string         `json:"content"`
	Images     []string       `json:"images,omitempty"`       // data: URIs, for vision models; held in memory only
	ToolCalls  []WireToolCall `json:"tool_calls,omitempty"`   // assistant turn that called tools
	ToolCallID string         `json:"tool_call_id,omitempty"` // tool turn answering one call
}

type InferenceParams struct {
	Temperature *float64 `json:"temperature,omitempty"`
	MaxTokens   int      `json:"max_tokens,omitempty"`
	Stop        []string `json:"stop,omitempty"`
	// Pass-through sampling and tool settings from the OpenAI-compatible API.
	TopP             *float64        `json:"top_p,omitempty"`
	Seed             *int            `json:"seed,omitempty"`
	PresencePenalty  *float64        `json:"presence_penalty,omitempty"`
	FrequencyPenalty *float64        `json:"frequency_penalty,omitempty"`
	ToolChoice       json.RawMessage `json:"tool_choice,omitempty"`
	ResponseFormat   json.RawMessage `json:"response_format,omitempty"`
	// JSONSchema, when set, makes llama-server constrain the answer to JSON of
	// this shape (response_format json_schema). Used only for a retry after a
	// local model's answer could not be parsed.
	JSONSchema json.RawMessage `json:"json_schema,omitempty"`
}

type InferenceOutput struct {
	Text         string         `json:"text"`
	FinishReason string         `json:"finish_reason"`
	PromptTokens int            `json:"prompt_tokens"`
	OutputTokens int            `json:"output_tokens"`
	TokensPerSec float64        `json:"tokens_per_s"`
	Model        string         `json:"model"`
	ToolCalls    []WireToolCall `json:"tool_calls,omitempty"`
}

// NodePause is "node.pause": the web UI's Pause menu on a node card.
type NodePause struct {
	Mode    string `json:"mode"`              // drain (running work finishes) | now (running work moves to other nodes) | resume
	Minutes int    `json:"minutes,omitempty"` // a timed pause; 0 means until resumed
}

// nodeFeatures lists the controller-to-node messages added after 0.8.0, so the
// controller can tell an older node apart instead of waiting for no answer.
var nodeFeatures = []string{"node.ask", "node.pause", "node.gen", "node.video"}
