//go:build windows || linux

package main

// `outpost tray` shows the node's status in the system tray (notification
// area) and lets the person at the machine pause work, join a different
// controller, open the web UI, or quit. It talks to the node over the local
// control port (node_control.go); it never talks to the controller itself.
//
// Linux: the node runs as the systemd service; the tray starts at desktop
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
	"sync"
	"time"

	"fyne.io/systray"
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
	mStatus   *systray.MenuItem
	mCtl      *systray.MenuItem
	mModel    *systray.MenuItem
	mWork     *systray.MenuItem
	mWeb      *systray.MenuItem
	mJoin     *systray.MenuItem
	mStart    *systray.MenuItem
	mQuit     *systray.MenuItem
	mQuitTray *systray.MenuItem
}

func runTray(args []string) error {
	fsf := flag.NewFlagSet("tray", flag.ExitOnError)
	data := fsf.String("data", defaultDataDir("node"), "the node's data directory")
	service := fsf.Bool("service", false, "the node runs as a Windows service")
	_ = fsf.Parse(args)
	if _, err := os.Stat(filepath.Join(*data, "node.json")); err != nil {
		return fmt.Errorf("no node config in %s; join first with `outpost install node --join TOKEN` (or --data DIR)", *data)
	}
	release, err := trayLock(*data)
	if err != nil {
		return err
	}
	defer release()
	trayDetachConsole(*data)
	t := &trayApp{dataDir: *data, service: *service, client: &http.Client{Timeout: 4 * time.Second}}
	systray.Run(t.onReady, func() {})
	return nil
}

func (t *trayApp) onReady() {
	systray.SetIcon(trayIconBytes(trayOffline))
	systray.SetTitle("")
	systray.SetTooltip("Outpost node")
	t.mStatus = systray.AddMenuItem("Outpost: checking…", "")
	t.mStatus.Disable()
	t.mCtl = systray.AddMenuItem("", "")
	t.mCtl.Disable()
	t.mModel = systray.AddMenuItem("", "")
	t.mModel.Disable()
	systray.AddSeparator()
	t.mWork = systray.AddMenuItemCheckbox("Accept new work", "Untick to stop the controller sending work to this machine", true)
	t.mWeb = systray.AddMenuItem("Open controller web UI", "")
	t.mJoin = systray.AddMenuItem("Join a different controller…", "Enter a join code from another controller")
	systray.AddSeparator()
	t.mStart = systray.AddMenuItem("Start Outpost", "Start the node again")
	t.mQuit = systray.AddMenuItem("Quit Outpost", "Stop the node and close this icon")
	t.mQuitTray = systray.AddMenuItem("Close this icon only", "The node keeps running")
	t.mStart.Hide()

	go t.loop()
	go func() {
		for {
			select {
			case <-t.mWork.ClickedCh:
				t.toggleWork()
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
				systray.Quit()
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
		systray.SetIcon(trayIconBytes(trayOffline))
		label := "Outpost: stopped"
		if starting {
			label = "Outpost: starting…"
		}
		systray.SetTooltip(label)
		t.mStatus.SetTitle(label)
		t.mCtl.Hide()
		t.mModel.Hide()
		t.mWork.Disable()
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
	state, word := trayOnline, "online"
	switch {
	case !st.Online:
		state, word = trayOffline, "connecting to controller…"
	case st.Paused:
		state, word = trayPaused, "paused (no new work)"
	case st.RunningJobs > 0:
		word = fmt.Sprintf("online · %d job(s) running", st.RunningJobs)
	}
	systray.SetIcon(trayIconBytes(state))
	systray.SetTooltip("Outpost node: " + word)
	t.mStatus.SetTitle("Outpost " + st.Version + ": " + word)
	host := st.Controller
	if u, err := url.Parse(st.Controller); err == nil && u.Host != "" {
		host = u.Host
	}
	t.mCtl.SetTitle("Controller: " + host)
	t.mCtl.Show()
	switch {
	case st.Lending != "":
		t.mModel.SetTitle("Lending memory to a pool")
	case st.Model != "" && st.ModelReady:
		t.mModel.SetTitle("Model: " + st.Model)
	case st.Model != "":
		t.mModel.SetTitle("Model: " + st.Model + " (loading)")
	default:
		t.mModel.SetTitle("No model loaded")
	}
	t.mModel.Show()
	if st.Paused {
		t.mWork.Uncheck()
	} else {
		t.mWork.Check()
	}
}

func (t *trayApp) toggleWork() {
	st := t.status()
	if st == nil {
		return
	}
	path := "/pause"
	if st.Paused {
		path = "/resume"
	}
	if nst, err := t.call("POST", path); err == nil {
		t.mu.Lock()
		t.last = nst
		t.mu.Unlock()
		t.render(nst)
	} else {
		log.Printf("tray: %s: %v", path, err)
	}
}

func (t *trayApp) openJoin() {
	cf, err := t.control()
	if err != nil {
		return
	}
	openURL(fmt.Sprintf("http://127.0.0.1:%d/join?t=%s", cf.Port, cf.Token))
}

func (t *trayApp) startNode() {
	if t.service {
		log.Printf("tray: the Outpost Node service starts at boot; to start it now run `sc start OutpostNode` as administrator")
		return
	}
	t.mu.Lock()
	t.starting = time.Now()
	t.mu.Unlock()
	if err := startNodeProcess(t.dataDir); err != nil {
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
	systray.Quit()
}

func trayIconBytes(st trayState) []byte {
	if runtime.GOOS == "windows" {
		return trayICO(st)
	}
	return trayPNG(st)
}

// nodeLogPath is where a node started by the tray writes its log.
func nodeLogPath(dataDir string) string { return filepath.Join(dataDir, "node.log") }

// openNodeLog opens the log for appending, starting over past 10 MB.
func openNodeLog(dataDir string) (*os.File, error) {
	p := nodeLogPath(dataDir)
	if fi, err := os.Stat(p); err == nil && fi.Size() > 10<<20 {
		_ = os.Rename(p, p+".old")
	}
	return os.OpenFile(p, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
}
