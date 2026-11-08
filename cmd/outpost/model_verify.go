package main

// Weekly integrity check of the controller's stored model files: each file is
// hashed again and compared with the SHA-256 recorded when it was downloaded.
// Big GGUF files on old disks do rot quietly, and a damaged copy would be
// pushed to every node. A mismatch or a missing file raises a banner and a
// "model_failed" notification; the model's Retry button downloads it again.
// The hash recorded at download time is the reference (it is checked against
// Hugging Face's size, not its published hash), so this finds damage after the
// download, not a bad download.

import (
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"time"
)

const modelVerifyEvery = 7 * 24 * time.Hour

func (c *Controller) verifyStatePath() string {
	return filepath.Join(c.modelsStoreDir(), "verify.json")
}

// verifyStore hashes every stored model file and returns one line per problem.
func (c *Controller) verifyStore() []string {
	c.mu.Lock()
	type job struct {
		name string
		f    ModelFile
	}
	var jobs []job
	for _, m := range c.models {
		if m.Status != "ready" || m.Storage != "controller" {
			continue
		}
		for _, f := range m.Files {
			jobs = append(jobs, job{m.Name, f})
		}
	}
	c.mu.Unlock()
	var problems []string
	for _, j := range jobs {
		p := filepath.Join(c.modelsStoreDir(), j.name, j.f.Path)
		st, err := os.Stat(p)
		if err != nil {
			problems = append(problems, fmt.Sprintf("%s: file %s is missing from the controller's store", j.name, j.f.Path))
			continue
		}
		if j.f.SHA256 == "" {
			continue
		}
		if st.Size() != j.f.Bytes && j.f.Bytes > 0 {
			problems = append(problems, fmt.Sprintf("%s: %s is %d bytes, expected %d", j.name, j.f.Path, st.Size(), j.f.Bytes))
			continue
		}
		sum, _, err := fileSHA256(p)
		if err != nil {
			problems = append(problems, fmt.Sprintf("%s: %s could not be read: %v", j.name, j.f.Path, err))
		} else if sum != j.f.SHA256 {
			problems = append(problems, fmt.Sprintf("%s: %s no longer matches its checksum (the disk may be failing)", j.name, j.f.Path))
		}
	}
	return problems
}

// modelVerifyLoop checks the store a few minutes after start-up if the last
// check is older than a week, then keeps checking weekly.
func (c *Controller) modelVerifyLoop() {
	time.Sleep(5 * time.Minute)
	for {
		var st struct {
			Last time.Time `json:"last"`
		}
		if b, err := os.ReadFile(c.verifyStatePath()); err == nil {
			_ = json.Unmarshal(b, &st)
		}
		if time.Since(st.Last) >= modelVerifyEvery {
			problems := c.verifyStore()
			st.Last = time.Now()
			if b, err := json.Marshal(st); err == nil {
				_ = writeFileAtomic(c.verifyStatePath(), b, 0o644)
			}
			for _, p := range problems {
				c.noteRecovered("Model check: " + p + ". Use Retry on the Models page to download it again.")
			}
			if len(problems) > 0 {
				go c.deliverNotification("model_failed", "Stored model damaged", fmt.Sprintf("%d problem(s) in the controller's model store; see the banner in the web UI.", len(problems)), "")
			}
			log.Printf("model store check: %d problem(s)", len(problems))
		}
		time.Sleep(6 * time.Hour)
	}
}
