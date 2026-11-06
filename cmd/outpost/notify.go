package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Notifications: a few events (question, plan ready, task done or failed,
// spending cap pause, node offline for 5 minutes) go to ntfy and/or a webhook,
// and are kept in a short in-memory list the browser polls to show desktop
// notifications. Every notification links to the task.

// NotifySettings is stored inside Settings. An event missing from Events is on.
type NotifySettings struct {
	Browser      bool            `json:"browser,omitempty"`
	NtfyURL      string          `json:"ntfy_url,omitempty"`    // topic URL, e.g. https://ntfy.sh/my-topic
	DigestHour   int             `json:"digest_hour,omitempty"` // hour (controller time) of the morning summary; 0 = 8, 24 = midnight
	NtfyTokenEnc string          `json:"ntfy_token_enc,omitempty"`
	WebhookURL   string          `json:"webhook_url,omitempty"`
	PublicURL    string          `json:"public_url,omitempty"` // base of the links; empty = the address the browser used
	Events       map[string]bool `json:"events,omitempty"`
}

var notifyEvents = []struct {
	ID    string `json:"id"`
	Label string `json:"label"`
}{
	{"question", "The planner asked a question"},
	{"plan", "A plan is ready to approve"},
	{"done", "A task finished"},
	{"failed", "A task failed"},
	{"cap", "The spending cap paused a task"},
	{"node_offline", "A node has been offline for 5 minutes"},
	{"backup_failed", "A nightly backup failed its check"},
	{"disk_low", "The controller's disk is nearly full"},
	{"update_failed", "A node update or rollback failed"},
	{"model_failed", "A model download or load failed"},
	{"digest", "Morning summary of what finished overnight"},
}

func (n NotifySettings) digestHour() int {
	switch {
	case n.DigestHour == 24:
		return 0 // midnight
	case n.DigestHour <= 0 || n.DigestHour > 23:
		return 8
	}
	return n.DigestHour
}

// offByDefault lists events that stay off until ticked.
var offByDefault = map[string]bool{"digest": true}

// eventPage is where a notification without a task links to.
var eventPage = map[string]string{"disk_low": "settings", "backup_failed": "settings", "update_failed": "updates", "model_failed": "models", "digest": "tasks"}

const nodeOfflineAfter = 5 * time.Minute

func (n NotifySettings) eventOn(id string) bool {
	on, ok := n.Events[id]
	if !ok {
		return !offByDefault[id]
	}
	return on
}

// Notification is one sent event, as the browser sees it.
type Notification struct {
	ID      int       `json:"id"`
	Event   string    `json:"event"`
	Title   string    `json:"title"`
	Message string    `json:"message"`
	TaskID  string    `json:"task_id,omitempty"`
	URL     string    `json:"url"`
	Time    time.Time `json:"time"`
}

type notifier struct {
	mu       sync.Mutex
	seq      int
	recent   []Notification
	host     string // address the browser last used, for links
	offlined map[string]bool
	client   *http.Client
}

func (n *notifier) httpClient() *http.Client {
	if n.client != nil {
		return n.client
	}
	return &http.Client{Timeout: 15 * time.Second}
}

// notifyTask sends an event about a task. Safe to call with any lock held.
func (c *Controller) notifyTask(t *Task, event, message string) {
	t.mu.Lock()
	id, title := t.ID, t.Title
	t.mu.Unlock()
	if strings.TrimSpace(title) == "" {
		title = "Task"
	}
	go c.deliverNotification(event, title, message, id)
}

func (c *Controller) notifyNodeOffline(name string) {
	go c.deliverNotification("node_offline", "Node offline", fmt.Sprintf("%s has been offline for 5 minutes.", name), "")
}

func (c *Controller) deliverNotification(event, title, message, taskID string) {
	c.mu.Lock()
	ns := c.settings.Notify
	enc := ns.NtfyTokenEnc
	c.mu.Unlock()
	if !ns.eventOn(event) {
		return
	}
	token, _ := c.decrypt(enc)
	c.nt.mu.Lock()
	base := strings.TrimRight(strings.TrimSpace(ns.PublicURL), "/")
	if base == "" && c.nt.host != "" {
		base = "https://" + c.nt.host
	}
	link := base + "/#/nodes"
	if p := eventPage[event]; p != "" {
		link = base + "/#/" + p
	}
	if taskID != "" {
		link = base + "/#/tasks/" + taskID
	}
	if base == "" {
		link = ""
	}
	c.nt.seq++
	n := Notification{ID: c.nt.seq, Event: event, Title: title, Message: message, TaskID: taskID, URL: link, Time: time.Now()}
	c.nt.recent = append(c.nt.recent, n)
	if len(c.nt.recent) > 50 {
		c.nt.recent = c.nt.recent[len(c.nt.recent)-50:]
	}
	c.nt.mu.Unlock()
	if err := c.sendChannels(ns, token, n); err != nil {
		log.Printf("notification %q: %v", event, err)
	}
}

// sendChannels posts to ntfy and the webhook, returning the first error.
func (c *Controller) sendChannels(ns NotifySettings, ntfyToken string, n Notification) error {
	var first error
	if u := strings.TrimSpace(ns.NtfyURL); u != "" {
		if err := c.sendNtfy(u, ntfyToken, n); err != nil {
			first = fmt.Errorf("ntfy: %w", err)
		}
	}
	if u := strings.TrimSpace(ns.WebhookURL); u != "" {
		if err := c.sendWebhook(u, n); err != nil && first == nil {
			first = fmt.Errorf("webhook: %w", err)
		}
	}
	return first
}

func (c *Controller) post(u, token string, body any) error {
	b, _ := json.Marshal(body)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, "POST", u, bytes.NewReader(b))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := c.nt.httpClient().Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		return fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	return nil
}

// sendNtfy uses ntfy's JSON publishing (POST to the server root with the topic
// in the body) so titles and messages can contain any characters.
func (c *Controller) sendNtfy(topicURL, token string, n Notification) error {
	u, err := url.Parse(topicURL)
	if err != nil || u.Host == "" {
		return fmt.Errorf("bad topic URL")
	}
	topic := strings.Trim(u.Path, "/")
	prefix := ""
	if i := strings.LastIndex(topic, "/"); i >= 0 { // server hosted under a sub-path
		prefix, topic = "/"+topic[:i], topic[i+1:]
	}
	if topic == "" {
		return fmt.Errorf("topic URL has no topic, e.g. https://ntfy.sh/my-topic")
	}
	root := *u
	root.Path, root.RawQuery = prefix+"/", ""
	body := map[string]any{"topic": topic, "title": n.Title, "message": n.Message, "tags": []string{"outpost"}}
	if n.URL != "" {
		body["click"] = n.URL
	}
	if n.Event == "failed" || n.Event == "node_offline" {
		body["priority"] = 4
	}
	return c.post(root.String(), token, body)
}

// sendWebhook posts JSON. "content" and "text" carry the same message so
// Discord and Slack style endpoints accept it as is.
func (c *Controller) sendWebhook(u string, n Notification) error {
	text := n.Title + ": " + n.Message
	if n.URL != "" {
		text += " " + n.URL
	}
	return c.post(u, "", map[string]any{
		"event": n.Event, "title": n.Title, "message": n.Message, "task_id": n.TaskID,
		"url": n.URL, "time": n.Time.Format(time.RFC3339), "content": text, "text": text,
	})
}

// handleNotifications returns notifications newer than ?since=ID. The first
// call (no since) returns none, only the current position, so a freshly opened
// tab does not replay history. It also remembers the address for links.
func (c *Controller) handleNotifications(w http.ResponseWriter, r *http.Request) {
	c.nt.mu.Lock()
	if r.Host != "" {
		c.nt.host = r.Host
	}
	last := c.nt.seq
	out := []Notification{}
	if s := r.URL.Query().Get("since"); s != "" {
		since, _ := strconv.Atoi(s)
		for _, n := range c.nt.recent {
			if n.ID > since {
				out = append(out, n)
			}
		}
	}
	c.nt.mu.Unlock()
	writeJSON(w, 200, map[string]any{"last": last, "notifications": out})
}

func (c *Controller) handleNotifyTest(w http.ResponseWriter, r *http.Request) {
	c.mu.Lock()
	ns := c.settings.Notify
	enc := ns.NtfyTokenEnc
	c.mu.Unlock()
	token, _ := c.decrypt(enc)
	c.nt.mu.Lock()
	if r.Host != "" {
		c.nt.host = r.Host
	}
	c.nt.mu.Unlock()
	n := Notification{Event: "test", Title: "Outpost", Message: "Test notification. Notifications are working.", Time: time.Now()}
	if base := strings.TrimRight(strings.TrimSpace(ns.PublicURL), "/"); base != "" {
		n.URL = base + "/"
	} else {
		n.URL = "https://" + r.Host + "/"
	}
	if err := c.sendChannels(ns, token, n); err != nil {
		writeJSON(w, 502, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, 200, map[string]bool{"ok": true})
}

// sweepOfflineNodes is called from offlineSweeper with c.mu held. A node that
// connected since the controller started and has been gone for 5 minutes
// triggers one notification until it returns.
func (c *Controller) sweepOfflineNodes(now time.Time) {
	for id, seen := range c.lastSeen {
		c.nt.mu.Lock()
		if c.nt.offlined == nil {
			c.nt.offlined = map[string]bool{}
		}
		_, online := c.conns[id]
		switch {
		case online:
			delete(c.nt.offlined, id)
		case now.Sub(seen) > nodeOfflineAfter && !c.nt.offlined[id]:
			c.nt.offlined[id] = true
			name := id
			if n := c.nodes[id]; n != nil && n.Name != "" {
				name = n.Name
			}
			c.notifyNodeOffline(name)
		}
		c.nt.mu.Unlock()
	}
}

// notifyView is the settings part the UI needs (the ntfy token is never sent back).
func (c *Controller) notifyView() map[string]any {
	ns := c.settings.Notify
	events := map[string]bool{}
	for _, e := range notifyEvents {
		events[e.ID] = ns.eventOn(e.ID)
	}
	return map[string]any{
		"notify_browser": ns.Browser, "notify_ntfy_url": ns.NtfyURL, "notify_has_ntfy_token": ns.NtfyTokenEnc != "",
		"notify_webhook_url": ns.WebhookURL, "notify_public_url": ns.PublicURL, "notify_events": events,
		"notify_event_list": notifyEvents, "notify_digest_hour": ns.digestHour(),
	}
}
