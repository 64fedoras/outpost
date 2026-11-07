package main

import (
	"net/http"
)

// ---------- web UI ----------

func (c *Controller) openRouterKey() string {
	c.mu.Lock()
	enc := c.settings.OpenRouterKeyEnc
	c.mu.Unlock()
	k, _ := c.decrypt(enc)
	return k
}

// handleOpenRouterModels returns OpenRouter's model list with prices per
// million tokens. ?refresh=1 downloads it again.
func (c *Controller) handleOpenRouterModels(w http.ResponseWriter, r *http.Request) {
	err := openRouterCatalog().Refresh(r.Context(), c.openRouterKey(), r.URL.Query().Get("refresh") == "1")
	list, _ := openRouterCatalog().List()
	if err != nil && len(list) == 0 {
		writeJSON(w, 502, map[string]string{"error": "could not load the OpenRouter model list: " + err.Error()})
		return
	}
	writeJSON(w, 200, map[string]any{"models": list})
}
