package main

import (
	"context"
	"crypto/tls"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"log"
	"net"
	"net/http"
	"net/netip"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/coder/websocket"

	"outpost"
)

var webFS = outpost.WebFS

const (
	maxJobOutputBytes = 1 << 20 // per job, kept on the controller for the UI
	maxJobsKept       = 200
	joinTokenTTL      = time.Hour
	heartbeatSeconds  = 10
)

type NodeRec struct {
	ID          string        `json:"id"`
	Name        string        `json:"name"`
	KeyHash     string        `json:"key_hash"`
	ExecEnabled bool          `json:"exec_enabled"`
	MaxJobs     int           `json:"max_jobs_override"`     // 0 = use the node's own value
	Sandbox     string        `json:"sandbox,omitempty"`     // this node's sandbox mode; empty = the global setting
	IdleUnload  string        `json:"idle_unload,omitempty"` // idle unload wait typed by the owner ("30m", "0"); empty = the global setting
	Added       time.Time     `json:"added"`
	Hello       *Hello        `json:"hello,omitempty"`
	LoadPrefs   *ModelLoad    `json:"load_prefs,omitempty"`    // last model loaded from the UI
	YieldGPU    bool          `json:"yield_gpu,omitempty"`     // unload the model while another program uses the GPU (gpu_yield.go)
	Profiles    []LoadProfile `json:"load_profiles,omitempty"` // named load settings (load_profiles.go)
	Pool        []string      `json:"pool,omitempty"`          // helper nodes lending memory to this node's model
	Bench       *NodeBench    `json:"bench,omitempty"`         // measured speed of the loaded model
	BenchFail   *BenchFail    `json:"bench_fail,omitempty"`    // why the last benchmark did not finish
	Benching    bool          `json:"benching,omitempty"`      // a benchmark is running
	Labels      []string      `json:"labels,omitempty"`        // owner's tags ("gpu", "office"); tasks can ask for one (labels.go)
	Hours       *WorkHours    `json:"hours,omitempty"`         // when the node takes work; nil = always (hours.go)
	LastAddr    string        `json:"last_addr,omitempty"`     // address it last connected from, for Wake-on-LAN (wol.go)
}

type nodeConn struct {
	conn *websocket.Conn
	mu   sync.Mutex
	addr string
}

func (nc *nodeConn) send(typ string, body any) error {
	b, err := newEnvelope(typ, body)
	if err != nil {
		return err
	}
	nc.mu.Lock()
	defer nc.mu.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	return nc.conn.Write(ctx, websocket.MessageText, b)
}

type outChunk struct {
	Seq    int    `json:"seq"`
	Stream string `json:"stream"`
	Data   string `json:"data"`
}

type JobRec struct {
	ID         string           `json:"id"`
	NodeID     string           `json:"node_id"`
	NodeName   string           `json:"node_name"`
	Kind       string           `json:"kind"`
	Command    string           `json:"command"`
	Status     string           `json:"status"` // dispatched | running | succeeded | failed | cancelled | timed_out | lost
	Created    time.Time        `json:"created"`
	Ended      *time.Time       `json:"ended,omitempty"`
	Chunks     []outChunk       `json:"chunks,omitempty"`
	OutBytes   int              `json:"-"`
	Truncated  bool             `json:"truncated"`
	Result     *JobResult       `json:"result,omitempty"`
	ExecOutput *ExecOutput      `json:"exec_output,omitempty"`
	Inference  *InferenceOutput `json:"inference,omitempty"`
	Cleaned    *WSClosed        `json:"cleaned,omitempty"`
	workspace  string
}

type Controller struct {
	mu          sync.Mutex
	inferStub   func(ctx context.Context, nodeID string, spec InferenceSpec) (*InferenceOutput, error)           // tests: stands in for node inference
	execStub    func(ctx context.Context, nodeID string, argv []string, inputs []FileEntry) (*ExecOutput, error) // tests: stands in for the best-of-N syntax check
	dataDir     string
	spend       *spendLedger // cloud spending ledger (spend.go); use spendLedger()
	spendOnce   sync.Once
	catShapes   map[string]*ModelShape // GGUF headers of catalog models, read from Hugging Face in the background
	advertise   string
	fingerprint string
	cert        tls.Certificate
	repinAcks   map[string]string
	admin       adminRecord
	setupAllow  []netip.Prefix // nil = private/LAN addresses only
	setupMu     sync.Mutex     // makes "no password yet" + "set it" one step

	nodes      map[string]*NodeRec
	conns      map[string]*nodeConn
	lastHB     map[string]*Heartbeat
	lastSeen   map[string]time.Time
	tokens     map[string]time.Time
	usedTok    map[string]usedToken // token id -> who used it (so the Add node dialog can move on)
	sessions   map[string]time.Time
	sessMeta   map[string]sessionMeta
	apiTokens  []apiToken
	loginFails map[string][]time.Time
	jobs       map[string]*JobRec
	jobOrder   []string
	releases   map[string]*Release
	ghState    ghUpdateState
	updates    map[string]*UpdateStatus

	settings   Settings
	nt         notifier
	waiters    map[string]chan *JobResult
	models     map[string]*ModelRec
	runtimes   map[string]*RuntimeRec
	rtCache    *runtimeCache
	gens       *genStore // image generation: queue, stored results, settings (controller_images.go)
	nodeOps    map[string]*nodeOpView
	pipelines  map[string]*pipeline         // model setups in progress; cancel stops one
	jobBlobs   map[string]map[string]string // job -> input path -> file on the controller, for nodes to download
	peerBad    map[string]time.Time         // nodes skipped as model sources until then (served data failing its hash)
	tasks      map[string]*Task
	recovered  recoveredNotes           // damaged state files found at start-up (recovered.go)
	asks       askWaiters               // node.ask requests waiting for their answer (node_ask.go)
	check      clusterCheck             // the last Check cluster run (clustercheck.go)
	lastBackup *backupCheck             // the newest scheduled backup, read back after writing
	storage    storageState             // disk use and clean-up (storage.go)
	rollout    *rolloutState            // the last one-node-first update (rollout.go)
	clockSkew  map[string]time.Duration // node clock minus controller clock, from heartbeats
	projects   map[string]*Project      // see projects.go
	chats      map[string]*Chat         // chat.go, guarded by chatMu
	chatMu     sync.Mutex
	// resumeWake wakes autoResumeInterrupted on node heartbeats (nudgeResume).
	resumeWake chan struct{}
	reserved   map[string]int // calls holding a node (see slotsLocked)
	cooldown   map[string]time.Time
	restarts   map[string]time.Time
	poolStale  map[string]time.Time // main node -> when a helper reconnected
	woken      map[string]time.Time // Wake-on-LAN packets sent, by node (wol.go)
	templates  []*Template          // saved tasks and schedules (templates.go), guarded by mu
	health     *healthLog           // node health score (node_health.go)
	healthOnce sync.Once
	card       *scoreCard // per-model results (scorecard.go)
	cardOnce   sync.Once
	stateHub   stateHub // pushed /api/state (state_stream.go)
	safeMode   bool     // crash-loop start: auto-resume and schedules off (safemode.go)
}

func runController(args []string) error {
	fsf := flag.NewFlagSet("controller", flag.ExitOnError)
	listen := fsf.String("listen", ":8443", "address to listen on")
	data := fsf.String("data", defaultDataDir("controller"), "data directory")
	advertise := fsf.String("advertise", "", "URL nodes use to reach this controller (default: https://<first LAN IP><listen port>)")
	resetPw := fsf.Bool("reset-password", false, "remove the admin password; the next visitor to the web UI chooses a new one")
	setupFrom := fsf.String("setup-allow-from", "", "who may choose the first admin password: comma-separated CIDRs, or \"any\" (default: private/LAN addresses only)")
	_ = fsf.Parse(args)

	if err := os.MkdirAll(*data, 0o700); err != nil {
		return err
	}
	log.SetOutput(io.MultiWriter(log.Writer(), ctlLog)) // keep the log's end for Download diagnostics
	cert, fp, err := loadOrCreateCert(*data)
	if err != nil {
		return fmt.Errorf("tls: %w", err)
	}
	c := &Controller{
		dataDir: *data, fingerprint: fp, cert: cert,
		nodes: map[string]*NodeRec{}, conns: map[string]*nodeConn{},
		lastHB: map[string]*Heartbeat{}, lastSeen: map[string]time.Time{},
		tokens: map[string]time.Time{}, usedTok: map[string]usedToken{}, sessions: map[string]time.Time{}, sessMeta: map[string]sessionMeta{}, loginFails: map[string][]time.Time{},
		jobs: map[string]*JobRec{}, updates: map[string]*UpdateStatus{},
		waiters: map[string]chan *JobResult{}, nodeOps: map[string]*nodeOpView{},
		pipelines: map[string]*pipeline{}, reserved: map[string]int{}, cooldown: map[string]time.Time{}, restarts: map[string]time.Time{}, poolStale: map[string]time.Time{},
		resumeWake: make(chan struct{}, 1),
	}
	checkTrial(c.agentDir(), os.Args[1:]) // roll back if a fresh update keeps crashing (update_trial.go)
	go trialHealthy(c.agentDir())
	launchNewest(c.agentDir(), os.Args[1:]) // switch to an installed update, if newer
	c.loadReleases()
	c.loadSessions()
	c.loadAPITokens()
	c.loadSettings()
	c.loadModels()
	c.loadRuntimes()
	c.loadGens()
	c.loadProjects()
	c.loadChats()
	c.loadTemplates()
	c.loadSavedCatalog()
	c.checkSafeMode()
	c.loadTasks()
	c.spendLedger() // load spend.json, or start it from the tasks on disk
	go c.autoResumeInterrupted(autoResumeWait, time.Second)
	c.advertise = *advertise
	if c.advertise == "" {
		_, port, _ := net.SplitHostPort(*listen)
		host := "127.0.0.1"
		if ips := localIPs(); len(ips) > 0 {
			host = ips[0].String()
		}
		c.advertise = "https://" + net.JoinHostPort(host, port)
	}
	if err := c.loadNodes(); err != nil {
		return err
	}
	c.migratePooling()
	allow, err := parseSetupAllow(*setupFrom)
	if err != nil {
		return err
	}
	c.setupAllow = allow
	if err := c.loadAdmin(*resetPw); err != nil {
		return err
	}

	mux := http.NewServeMux()
	static, _ := fs.Sub(webFS, "web")
	files := http.FileServer(http.FS(static))
	mux.Handle("GET /", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// The UI ships inside the binary; make browsers revalidate so an update shows at once.
		w.Header().Set("Cache-Control", "no-cache")
		files.ServeHTTP(w, r)
	}))
	mux.HandleFunc("GET /licenses", handleLicenses)
	mux.HandleFunc("GET /healthz", c.handleHealth)
	mux.HandleFunc("POST /api/login", c.handleLogin)
	mux.HandleFunc("GET /api/setup", c.handleSetupStatus)
	mux.HandleFunc("POST /api/setup", c.handleSetup)
	mux.HandleFunc("POST /api/logout", c.handleLogout)
	mux.HandleFunc("GET /api/sessions", c.authSession(c.handleListSessions))
	mux.HandleFunc("DELETE /api/sessions/{id}", c.authSession(c.handleRevokeSession))
	mux.HandleFunc("POST /api/sessions/logout-all", c.authSession(c.handleLogoutAll))
	mux.HandleFunc("GET /api/tls", c.authSession(c.handleTLSInfo))
	mux.HandleFunc("POST /api/tls", c.authSession(c.handleSetTLS))
	mux.HandleFunc("POST /api/backup", c.authSession(c.handleBackup))
	mux.HandleFunc("GET /api/config/export", c.authSession(c.handleConfigExport))
	mux.HandleFunc("POST /api/config/import", c.authSession(c.handleConfigImport))
	mux.HandleFunc("POST /api/backup/schedule", c.authSession(c.handleBackupSchedule))
	mux.HandleFunc("GET /api/apitokens", c.authSession(c.handleListAPITokens))
	mux.HandleFunc("POST /api/apitokens", c.authSession(c.handleNewAPIToken))
	mux.HandleFunc("DELETE /api/apitokens/{id}", c.authSession(c.handleRevokeAPIToken))
	mux.HandleFunc("GET /api/state", c.auth(c.handleState))
	mux.HandleFunc("GET /api/state/events", c.authSession(c.handleStateEvents))
	mux.HandleFunc("POST /api/recovered/dismiss", c.auth(c.handleDismissRecovered))
	mux.HandleFunc("POST /api/diagnostics", c.authSession(c.handleDiagnostics))
	mux.HandleFunc("GET /api/scorecard", c.auth(c.handleScorecard))
	mux.HandleFunc("GET /api/storage", c.auth(c.handleStorage))
	mux.HandleFunc("POST /api/storage", c.authSession(c.handleStorageSettings))
	mux.HandleFunc("GET /api/nodes/{id}/logs", c.auth(c.handleNodeLogs))
	mux.HandleFunc("GET /api/cluster/check", c.auth(c.handleGetClusterCheck))
	mux.HandleFunc("POST /api/cluster/check", c.auth(c.handleStartClusterCheck))
	mux.HandleFunc("POST /api/tokens", c.auth(c.handleNewToken))
	mux.HandleFunc("GET /api/tokens/{id}", c.auth(c.handleTokenStatus))
	mux.HandleFunc("POST /api/nodes/{id}/settings", c.auth(c.handleNodeSettings))
	mux.HandleFunc("POST /api/nodes/{id}/wake", c.auth(c.handleWakeNode))
	mux.HandleFunc("DELETE /api/nodes/{id}", c.auth(c.handleNodeDelete))
	mux.HandleFunc("POST /api/nodes/{id}/jobs", c.auth(c.handleNewJob))
	mux.HandleFunc("GET /api/jobs/{id}", c.auth(c.handleGetJob))
	mux.HandleFunc("POST /api/jobs/{id}/cancel", c.auth(c.handleCancelJob))
	mux.HandleFunc("POST /api/releases", c.auth(c.handleUploadRelease))
	mux.HandleFunc("DELETE /api/releases/{platform}", c.auth(c.handleDeleteRelease))
	mux.HandleFunc("POST /api/nodes/{id}/update", c.auth(c.handleUpdateNode))
	mux.HandleFunc("POST /api/update-all", c.auth(c.handleUpdateAll))
	mux.HandleFunc("POST /api/controller/update", c.auth(c.handleUpdateController))
	mux.HandleFunc("POST /api/github-updates/check", c.auth(c.handleCheckGitHub))
	mux.HandleFunc("POST /api/github-updates/install", c.auth(c.handleInstallGitHub))
	mux.HandleFunc("POST /api/github-updates/test", c.auth(c.handleTestGitHub))
	mux.HandleFunc("POST /api/controller/rollback", c.auth(c.handleRollbackController))
	mux.HandleFunc("POST /api/nodes/{id}/rollback", c.auth(c.handleRollbackNode))
	mux.HandleFunc("POST /api/nodes/{id}/restart", c.auth(c.handleRestartNode))
	mux.HandleFunc("POST /api/nodes/{id}/pause", c.auth(c.handlePauseNode))
	mux.HandleFunc("GET /v1/releases/{platform}", c.handleServeRelease)
	mux.HandleFunc("GET /v1/runtime/{variant}", c.handleServeRuntime)
	c.installRoutes(mux)
	c.toolchainRoutes(mux)
	mux.HandleFunc("GET /v1/models/{name}/files/{index}", c.handleServeModelFile)
	mux.HandleFunc("GET /v1/jobs/{id}/inputs", c.handleServeJobInput)
	mux.HandleFunc("GET /api/settings", c.auth(c.handleGetSettings))
	mux.HandleFunc("POST /api/settings", c.auth(c.handleSaveSettings))
	mux.HandleFunc("GET /api/notifications", c.auth(c.handleNotifications))
	mux.HandleFunc("POST /api/notifications/test", c.auth(c.handleNotifyTest))
	mux.HandleFunc("GET /api/openrouter/models", c.auth(c.handleOpenRouterModels))
	mux.HandleFunc("POST /api/password", c.auth(c.handleChangePassword))
	mux.HandleFunc("GET /api/models", c.auth(c.handleListModels))
	mux.HandleFunc("POST /api/models", c.auth(c.handleAddModel))
	mux.HandleFunc("DELETE /api/models/{name}", c.auth(c.handleDeleteModel))
	mux.HandleFunc("POST /api/models/{name}/retry", c.auth(c.handleRetryModel))
	mux.HandleFunc("POST /api/models/{name}/store", c.auth(c.handleStoreModel))
	mux.HandleFunc("POST /api/models/{name}/tier", c.auth(c.handleSetModelTier))
	mux.HandleFunc("POST /api/models/{name}/unstore", c.auth(c.handleUnstoreModel))
	mux.HandleFunc("POST /api/models/{name}/release", c.auth(c.handleReleaseModel))
	mux.HandleFunc("GET /api/hf/files", c.auth(c.handleHFFiles))
	mux.HandleFunc("POST /api/runtime/check", c.auth(c.handleCheckRuntime))
	mux.HandleFunc("GET /api/nodes/{id}/load-options", c.auth(c.handleLoadOptions))
	mux.HandleFunc("POST /api/nodes/{id}/load", c.auth(c.handleLoadModel))
	mux.HandleFunc("POST /api/nodes/{id}/benchmark", c.auth(c.handleBenchmarkNode))
	mux.HandleFunc("POST /api/nodes/{id}/health/reset", c.auth(c.handleResetHealth))
	mux.HandleFunc("POST /api/nodes/{id}/profiles", c.auth(c.handleSaveLoadProfile))
	mux.HandleFunc("DELETE /api/nodes/{id}/profiles/{name}", c.auth(c.handleDeleteLoadProfile))
	mux.HandleFunc("POST /api/nodes/{id}/unload", c.auth(c.handleUnloadModel))
	mux.HandleFunc("DELETE /api/nodes/{id}/models/{name}", c.auth(c.handleRemoveNodeModel))
	mux.HandleFunc("POST /api/nodes/{id}/models/cleanup", c.auth(c.handleCleanupNodeModels))
	c.projectRoutes(mux)
	c.chatRoutes(mux)
	c.templateRoutes(mux)
	c.openAIRoutes(mux)
	c.imageRoutes(mux)
	mux.HandleFunc("GET /api/tasks", c.auth(c.handleListTasks))
	mux.HandleFunc("POST /api/tasks", c.auth(c.handleCreateTask))
	mux.HandleFunc("GET /api/tasks/{id}", c.auth(c.handleGetTask))
	mux.HandleFunc("DELETE /api/tasks/{id}", c.auth(c.handleDeleteTask))
	mux.HandleFunc("GET /api/spending", c.auth(c.handleSpending))
	mux.HandleFunc("POST /api/spending/cap", c.authSession(c.handleSpendingCap))
	mux.HandleFunc("POST /api/tasks/{id}/meta", c.auth(c.handleTaskMeta))
	mux.HandleFunc("POST /api/tasks/{id}/approve", c.auth(c.handleApproveTask))
	mux.HandleFunc("POST /api/tasks/{id}/difficulty", c.auth(c.handleSetDifficulty))
	mux.HandleFunc("POST /api/tasks/{id}/replan", c.auth(c.handleReplanTask))
	mux.HandleFunc("POST /api/tasks/{id}/messages", c.auth(c.handleTaskMessage))
	mux.HandleFunc("POST /api/tasks/{id}/messages/{msg}/edit", c.auth(c.handleEditMessage))
	mux.HandleFunc("POST /api/tasks/{id}/send-now", c.auth(c.handleSendNow))
	mux.HandleFunc("DELETE /api/tasks/{id}/queued/{note}", c.auth(c.handleRemoveQueued))
	mux.HandleFunc("POST /api/tasks/{id}/answers", c.auth(c.handleTaskAnswers))
	mux.HandleFunc("POST /api/tasks/{id}/agent-answer", c.auth(c.handleAgentAnswer))
	mux.HandleFunc("POST /api/tasks/{id}/cancel", c.auth(c.handleCancelTask))
	mux.HandleFunc("POST /api/tasks/{id}/resume", c.auth(c.handleResumeTask))
	mux.HandleFunc("POST /api/tasks/{id}/rerun", c.auth(c.handleRerunPlan))
	mux.HandleFunc("POST /api/tasks/{id}/subtasks/{sid}/redo", c.auth(c.handleRedoSubtask))
	mux.HandleFunc("POST /api/tasks/{id}/plan/edit", c.auth(c.handleEditPlan))
	mux.HandleFunc("GET /api/tasks/{id}/events", c.auth(c.handleTaskEvents))
	mux.HandleFunc("GET /api/tasks/{id}/file", c.auth(c.handleTaskFile))
	mux.HandleFunc("POST /api/tasks/{id}/file", c.authSession(c.handleSaveTaskFile))
	mux.HandleFunc("GET /api/tasks/{id}/patch", c.auth(c.handleTaskPatch))
	mux.HandleFunc("GET /api/tasks/{id}/handoff", c.auth(c.handleTaskHandoff))
	mux.HandleFunc("GET /api/tasks/{id}/artifact", c.auth(c.handleTaskArtifact))
	mux.HandleFunc("GET /api/tasks/{id}/artifacts.zip", c.auth(c.handleTaskArtifactsZip))
	mux.HandleFunc("POST /api/tasks/{id}/publish", c.auth(c.handlePublishArtifacts))
	mux.HandleFunc("POST /api/tasks/{id}/zip", c.auth(c.handleTaskZip))
	mux.HandleFunc("GET /api/tasks/{id}/rounds", c.auth(c.handleTaskRounds))
	mux.HandleFunc("GET /api/tasks/{id}/changes", c.auth(c.handleTaskChanges))
	mux.HandleFunc("POST /api/tasks/{id}/undo", c.auth(c.handleUndoRound))
	mux.HandleFunc("GET /api/tasks/{id}/image", c.auth(c.handleTaskImage))
	mux.HandleFunc("GET /api/tasks/{id}/shot", c.auth(c.handleTaskShot))
	mux.HandleFunc("POST /api/tasks/{id}/pick", c.auth(c.handlePickVariant))
	mux.HandleFunc("GET /api/tasks/{id}/download", c.auth(c.handleDownloadTask))
	mux.HandleFunc("POST /v1/node/join", c.handleJoin)
	mux.HandleFunc("GET /v1/node/ws", c.handleNodeWS)

	srv := &http.Server{
		Addr:              *listen,
		Handler:           logRequests(secureHeaders(mux)),
		TLSConfig:         &tls.Config{GetCertificate: c.getCertificate, MinVersion: tls.VersionTLS12},
		ReadHeaderTimeout: 10 * time.Second,
	}
	go c.offlineSweeper()
	go c.poolKeeper()
	go c.backupLoop()
	go c.storageLoop()
	go c.digestLoop()
	go c.weeklyLoop()
	go c.ghLoop()
	go c.benchLoop()
	go c.catalogShapeLoop()
	go c.hoursLoop()
	go c.genLoop()
	go c.templateLoop()
	go c.modelVerifyLoop()
	go c.stallLoop()

	log.Printf("outpost controller %s listening on %s", Version, *listen)
	log.Printf("web UI:           %s", c.advertise)
	log.Printf("cert fingerprint: %s", fp)
	log.Printf("data dir:         %s", *data)
	if c.needsSetup() {
		log.Printf("no admin password yet: open the web UI to choose one (first-run setup allowed from: %s)", describeSetupAllow(c.setupAllow))
	}
	done := make(chan struct{})
	go c.handleSignals(srv, done)
	err = srv.ListenAndServeTLS("", "")
	if errors.Is(err, http.ErrServerClosed) {
		<-done
		return nil
	}
	return err
}

func logRequests(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/api/state") || strings.HasPrefix(r.URL.Path, "/api/jobs/") && r.Method == "GET" {
			h.ServeHTTP(w, r) // polled constantly; don't log
			return
		}
		if strings.HasPrefix(r.URL.Path, "/api/") || strings.HasPrefix(r.URL.Path, "/v1/") {
			log.Printf("%s %s from %s", r.Method, r.URL.Path, r.RemoteAddr)
		}
		h.ServeHTTP(w, r)
	})
}
