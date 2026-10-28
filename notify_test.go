package main

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"
)

type hit struct {
	path, auth string
	body       map[string]any
}

func receiver(t *testing.T) (*httptest.Server, func(n int) []hit) {
	t.Helper()
	var mu sync.Mutex
	var hits []hit
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		var m map[string]any
		_ = json.Unmarshal(b, &m)
		mu.Lock()
		hits = append(hits, hit{r.URL.Path, r.Header.Get("Authorization"), m})
		mu.Unlock()
	}))
	t.Cleanup(srv.Close)
	return srv, func(n int) []hit {
		for i := 0; i < 100; i++ {
			mu.Lock()
			l := len(hits)
			out := append([]hit(nil), hits...)
			mu.Unlock()
			if l >= n {
				return out
			}
			time.Sleep(20 * time.Millisecond)
		}
		mu.Lock()
		defer mu.Unlock()
		return append([]hit(nil), hits...)
	}
}

func TestNotifyChannels(t *testing.T) {
	ntfy, ntfyHits := receiver(t)
	hook, hookHits := receiver(t)
	c := &Controller{dataDir: t.TempDir()}
	enc, _ := c.encrypt("tk_secret")
	c.settings.Notify = NotifySettings{
		NtfyURL: ntfy.URL + "/sub/my-topic", NtfyTokenEnc: enc, WebhookURL: hook.URL,
		PublicURL: "https://outpost.lan:8443/", Events: map[string]bool{"plan": false},
	}
	task := &Task{ID: "t123", Title: "Dice roller"}

	c.notifyTask(task, "plan", "ignored, event is off")
	c.notifyTask(task, "done", "Finished.")

	nh := ntfyHits(1)
	if len(nh) != 1 {
		t.Fatalf("want 1 ntfy post, got %d", len(nh))
	}
	if nh[0].path != "/sub/" || nh[0].body["topic"] != "my-topic" || nh[0].body["title"] != "Dice roller" ||
		nh[0].body["click"] != "https://outpost.lan:8443/#/tasks/t123" || nh[0].auth != "Bearer tk_secret" {
		t.Fatalf("bad ntfy post: %+v", nh[0])
	}
	wh := hookHits(1)
	if len(wh) != 1 || wh[0].body["event"] != "done" || wh[0].body["task_id"] != "t123" ||
		wh[0].body["url"] != "https://outpost.lan:8443/#/tasks/t123" || wh[0].body["content"] == "" {
		t.Fatalf("bad webhook post: %+v", wh)
	}
	time.Sleep(100 * time.Millisecond)
	if len(ntfyHits(2)) != 1 {
		t.Fatal("the switched-off event was sent")
	}
}

func TestNotifyBrowserFeed(t *testing.T) {
	c := &Controller{dataDir: t.TempDir()}
	get := func(q string) (int, []Notification) {
		req := httptest.NewRequest("GET", "/api/notifications"+q, nil)
		req.Host = "ctl.lan:8443"
		w := httptest.NewRecorder()
		c.handleNotifications(w, req)
		var r struct {
			Last          int            `json:"last"`
			Notifications []Notification `json:"notifications"`
		}
		_ = json.Unmarshal(w.Body.Bytes(), &r)
		return r.Last, r.Notifications
	}
	last, list := get("")
	if last != 0 || len(list) != 0 {
		t.Fatal("first poll must not replay history")
	}
	c.deliverNotification("failed", "Build", "tests failed", "t9")
	_, list = get("?since=" + "0")
	if len(list) != 1 || list[0].URL != "https://ctl.lan:8443/#/tasks/t9" {
		t.Fatalf("bad feed: %+v", list)
	}
	if _, list = get("?since=1"); len(list) != 0 {
		t.Fatal("already-seen notification returned again")
	}
}

func TestNodeOfflineNotifiesOnce(t *testing.T) {
	c := &Controller{dataDir: t.TempDir(), conns: map[string]*nodeConn{}, nodes: map[string]*NodeRec{"n1": {Name: "node01"}},
		lastSeen: map[string]time.Time{"n1": time.Now().Add(-6 * time.Minute)}}
	c.sweepOfflineNodes(time.Now())
	c.sweepOfflineNodes(time.Now())
	time.Sleep(150 * time.Millisecond)
	c.nt.mu.Lock()
	defer c.nt.mu.Unlock()
	if len(c.nt.recent) != 1 || c.nt.recent[0].Event != "node_offline" {
		t.Fatalf("want one node_offline notification, got %+v", c.nt.recent)
	}
}
