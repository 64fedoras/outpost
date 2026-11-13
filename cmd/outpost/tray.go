//go:build windows || linux || darwin

package main

// `outpost tray` shows the node's status in the system tray (notification
// area) and lets the person at the machine pause work, join a different
// controller, open the web UI, or quit. It talks to the node over the local
// control port (node_control.go); it never talks to the controller itself.
//
// macOS: the node is a LaunchAgent and the tray one too (menu bar item, see
// tray_ui_darwin.go). Linux: the node runs as the systemd service; the tray starts at desktop
// login (~/.config/autostart). Windows: the tray starts at login (Run key)
// and starts the node hidden in the background if it is not running.

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"
)

type trayApp struct {
	dataDir string
	client  *http.Client
	service bool // the node runs as a Windows service; the tray never starts it

	mu        sync.Mutex
	last      *controlStatus
	starting  time.Time
	quitting  bool
	shownKey  string
	mStatus   *trayItem
	mCtl      *trayItem
	mModel    *trayItem
	mWork     *trayItem // Accept new work
	mFinish   *trayItem // Finish current work, then pause
	mStop     *trayItem // Pause now (cancels running work)
	mUnload   *trayItem
	mWeb      *trayItem
	mJoin     *trayItem
	mStart    *trayItem
	mQuit     *trayItem
	mQuitTray *trayItem
}

func runTray(args []string) error {
	fsf := flag.NewFlagSet("tray", flag.ExitOnError)
	data := fsf.String("data", defaultDataDir("node"), "the node's data directory")
	service := fsf.Bool("service", false, "the node runs as a Windows service")
	_ = fsf.Parse(args)
	if _, err := os.Stat(filepath.Join(*data, "node.json")); err != nil {
		return fmt.Errorf("no node config in %s; join first with `outpost install node --join TOKEN` (or --data DIR)", *data)
	}
	traySwitchToNewest(*data, args)
	release, err := trayLock(*data)
	if err != nil {
		return err
	}
	defer release()
	trayDetachConsole(*data)
	t := &trayApp{dataDir: *data, service: *service, client: &http.Client{Timeout: 4 * time.Second}}
	trayRunUI(t.onReady)
	return nil
}

func (t *trayApp) onReady() {
	traySetIcon(trayIconBytes(trayOffline))
	traySetTitle("")
	traySetTooltip("Outpost node")
	t.mStatus = trayAddItem("Outpost: checking…", "")
	t.mStatus.Disable()
	t.mCtl = trayAddItem("", "")
	t.mCtl.Disable()
	t.mModel = trayAddItem("", "")
	t.mModel.Disable()
	trayAddSeparator()
	t.mWork = trayAddCheckbox("Accept new work", "The controller can send work to this machine", true)
	t.mFinish = trayAddCheckbox("Finish current work, then pause", "No new work; jobs already running here finish first", false)
	t.mStop = trayAddCheckbox("Pause now (cancels running work)", "No new work; running jobs stop and go to other nodes", false)
	t.mUnload = trayAddItem("Unload model", "Free this machine's memory now; the model loads again when the next task needs it")
	t.mWeb = trayAddItem("Open controller web UI", "")
	t.mJoin = trayAddItem("Join a different controller…", "Enter a join code from another controller")
	trayAddSeparator()
	t.mStart = trayAddItem("Start Outpost", "Start the node again")
	t.mQuit = trayAddItem("Quit Outpost", "Stop the node and close this icon")
	t.mQuitTray = trayAddItem("Close this icon only", "The node keeps running")
	t.mStart.Hide()

	go t.loop()
	go func() {
		for {
			select {
			case <-t.mWork.ClickedCh:
				t.setWorkMode("/resume")
			case <-t.mFinish.ClickedCh:
				t.setWorkMode("/pause")
			case <-t.mStop.ClickedCh:
				t.setWorkMode("/pause-now")
			case <-t.mUnload.ClickedCh:
				t.unload()
			case <-t.mWeb.ClickedCh:
				if st := t.status(); st != nil && st.Controller != "" {
					openURL(st.Controller)
				}
			case <-t.mJoin.ClickedCh:
				t.openJoin()
			case <-t.mStart.ClickedCh:
				t.startNode()
			case <-t.mQuit.ClickedCh:
				t.quit()
			case <-t.mQuitTray.ClickedCh:
				trayQuitUI()
			}
		}
	}()
}

func (t *trayApp) status() *controlStatus {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.last
}

func (t *trayApp) control() (*controlFile, error) {
	b, err := os.ReadFile(controlPath(t.dataDir))
	if err != nil {
		return nil, err
	}
	var cf controlFile
	if err := json.Unmarshal(b, &cf); err != nil || cf.Port == 0 {
		return nil, errors.New("bad control file")
	}
	return &cf, nil
}

// call sends one request to the node's control port.
func (t *trayApp) call(method, path string) (*controlStatus, error) {
	cf, err := t.control()
	if err != nil {
		return nil, err
	}
	req, _ := http.NewRequest(method, fmt.Sprintf("http://127.0.0.1:%d%s", cf.Port, path), nil)
	req.Header.Set("X-Outpost-Token", cf.Token)
	resp, err := t.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		var e struct {
			Error string `json:"error"`
		}
		if json.NewDecoder(resp.Body).Decode(&e) == nil && e.Error != "" {
			return nil, fmt.Errorf("node said %s: %s", resp.Status, e.Error)
		}
		return nil, fmt.Errorf("node said %s", resp.Status)
	}
	var st controlStatus
	if path == "/quit" {
		return nil, nil
	}
	if err := json.NewDecoder(resp.Body).Decode(&st); err != nil {
		return nil, err
	}
	return &st, nil
}

func (t *trayApp) loop() {
	first, misses := true, 0
	for {
		st, err := t.call("GET", "/status")
		t.mu.Lock()
		if err != nil {
			st = nil
		}
		t.last = st
		quitting := t.quitting
		t.mu.Unlock()
		if quitting {
			return
		}
		// On Windows nothing else runs the node, so the tray keeps it running
		// (unless someone chose Quit, which closes the tray too).
		t.mu.Lock()
		recent := time.Since(t.starting) < 30*time.Second
		t.mu.Unlock()
		// A restart or update hands over between processes in a second or two,
		// so only start the node after it has been gone for three checks.
		if st == nil {
			misses++
		} else {
			misses = 0
		}
		if st == nil && runtime.GOOS == "windows" && !t.service && (first || (!recent && misses >= 3)) {
			t.startNode()
			misses = 0
		}
		first = false
		t.render(st)
		time.Sleep(3 * time.Second)
	}
}

func (t *trayApp) render(st *controlStatus) {
	key := "stopped"
	if st != nil {
		b, _ := json.Marshal(st)
		key = string(b)
	}
	t.mu.Lock()
	starting := time.Since(t.starting) < 30*time.Second
	same := key == t.shownKey && !starting
	t.shownKey = key
	t.mu.Unlock()
	if same {
		return
	}
	if st == nil {
		traySetIcon(trayIconBytes(trayOffline))
		label := "Outpost: stopped"
		if starting {
			label = "Outpost: starting…"
		}
		traySetTooltip(label)
		t.mStatus.SetTitle(label)
		t.mCtl.Hide()
		t.mModel.Hide()
		t.mWork.Disable()
		t.mFinish.Disable()
		t.mStop.Disable()
		t.mUnload.Hide()
		t.mWeb.Disable()
		t.mJoin.Disable()
		t.mQuit.Hide()
		t.mStart.Show()
		return
	}
	t.mStart.Hide()
	t.mQuit.Show()
	t.mWeb.Enable()
	t.mJoin.Enable()
	t.mWork.Enable()
	t.mFinish.Enable()
	t.mStop.Enable()
	state, word := trayOnline, "online"
	switch {
	case !st.Online && st.Problem == problemLocalNetwork:
		state, word = trayOffline, "blocked by macOS Local Network setting"
	case !st.Online:
		state, word = trayOffline, "connecting to controller…"
	case st.Paused && st.RunningJobs > 0:
		state, word = trayPaused, fmt.Sprintf("finishing %d job(s), then paused", st.RunningJobs)
	case st.Paused:
		state, word = trayPaused, "paused (no new work)"
	case st.RunningJobs > 0:
		word = fmt.Sprintf("online · %d job(s) running", st.RunningJobs)
	}
	traySetIcon(trayIconBytes(state))
	traySetTooltip("Outpost node: " + word)
	t.mStatus.SetTitle("Outpost " + st.Version + ": " + word)
	host := st.Controller
	if u, err := url.Parse(st.Controller); err == nil && u.Host != "" {
		host = u.Host
	}
	t.mCtl.SetTitle("Controller: " + host)
	t.mCtl.Show()
	switch {
	case !st.Online && st.Problem == problemLocalNetwork:
		t.mModel.SetTitle("Allow it: System Settings > Privacy & Security > Local Network")
	case st.Lending != "":
		t.mModel.SetTitle("Lending memory to a pool")
	case st.Model != "" && st.ModelReady:
		t.mModel.SetTitle("Model: " + st.Model)
	case st.Model != "":
		t.mModel.SetTitle("Model: " + st.Model + " (loading)")
	case st.ModelCold != "":
		t.mModel.SetTitle("Model: " + st.ModelCold + " (unloaded, loads on next task)")
	default:
		t.mModel.SetTitle("No model loaded")
	}
	t.mModel.Show()
	title, ok := unloadItemState(st)
	t.mUnload.SetTitle(title)
	if ok {
		t.mUnload.Enable()
	} else {
		t.mUnload.Disable()
	}
	t.mUnload.Show()
	checks := map[string]*trayItem{"ready": t.mWork, "finish": t.mFinish, "paused": t.mStop}
	mode := workMode(st)
	for m, it := range checks {
		if m == mode {
			it.Check()
		} else {
			it.Uncheck()
		}
	}
}

// workMode is which of the three work items is ticked: "ready" (taking work),
// "finish" (paused, jobs still finishing) or "paused" (paused, nothing running).
func workMode(st *controlStatus) string {
	switch {
	case !st.Paused:
		return "ready"
	case st.RunningJobs > 0:
		return "finish"
	}
	return "paused"
}

// setWorkMode asks the node to resume, pause or pause now, then redraws the
// menu even when nothing changed (some desktops flip a clicked checkbox by themselves).
func (t *trayApp) setWorkMode(path string) {
	if t.status() == nil {
		return
	}
	nst, err := t.call("POST", path)
	if err != nil {
		log.Printf("tray: %s: %v", path, err)
		return
	}
	t.mu.Lock()
	t.last = nst
	t.shownKey = ""
	t.mu.Unlock()
	t.render(nst)
}

// unloadItemState is the Unload model item's title and whether it can be clicked.
func unloadItemState(st *controlStatus) (string, bool) {
	switch {
	case st.Model == "":
		return "Unload model", false
	case st.Pooled:
		return "Unload model (pooled: use the web UI)", false
	case !st.ModelReady:
		return "Unload model (still loading)", false
	case st.RunningJobs > 0:
		return "Unload model (after the running job)", false
	}
	return "Unload model", true
}

func (t *trayApp) unload() {
	nst, err := t.call("POST", "/unload")
	if err != nil {
		log.Printf("tray: unload: %v", err)
		return
	}
	t.mu.Lock()
	t.last = nst
	t.mu.Unlock()
	t.render(nst)
}

func (t *trayApp) openJoin() {
	cf, err := t.control()
	if err != nil {
		return
	}
	openURL(fmt.Sprintf("http://127.0.0.1:%d/join?t=%s", cf.Port, cf.Token))
}

func (t *trayApp) startNode() {
	t.mu.Lock()
	t.starting = time.Now()
	t.mu.Unlock()
	start := startNodeProcess
	if t.service {
		start = startNodeService // the Windows service, after Quit stopped it
	}
	if err := start(t.dataDir); err != nil {
		log.Printf("tray: starting the node: %v", err)
	}
	t.render(nil)
}

func (t *trayApp) quit() {
	t.mu.Lock()
	t.quitting = true
	t.mu.Unlock()
	if _, err := t.call("POST", "/quit"); err != nil {
		log.Printf("tray: quit: %v", err)
	}
	trayQuitUI()
}

func trayIconBytes(st trayState) []byte {
	if runtime.GOOS == "windows" {
		return trayICO(st)
	}
	return trayPNG(st)
}

// nodeLogPath is where a node started by the tray writes its log.
func nodeLogPath(dataDir string) string { return filepath.Join(dataDir, "node.log") }

// consoleLogPath is where a node started by a tray or LaunchAgent sends its
// raw output (crash traces); its normal log goes to node.log, which it rotates.
func consoleLogPath(logPath string) string {
	return strings.TrimSuffix(logPath, ".log") + "-console.log"
}

// openNodeConsoleLog starts node-console.log afresh for a new node process.
func openNodeConsoleLog(dataDir string) (*os.File, error) {
	return os.OpenFile(consoleLogPath(nodeLogPath(dataDir)), os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
}
