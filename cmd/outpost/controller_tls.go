package main

import (
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"time"
)

// Your own TLS certificate (0.7). Nodes pin the controller by certificate
// fingerprint, so a swap first tells every connected node the new fingerprint
// ("node.repin", acknowledged), and only then starts serving the new
// certificate. Nodes that were offline keep the old pin and are re-pinned by
// hand with `outpost repin` (see README).

func (c *Controller) fingerprintNow() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.fingerprint
}

// getCertificate serves the current certificate, so a swap needs no restart.
func (c *Controller) getCertificate(*tls.ClientHelloInfo) (*tls.Certificate, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	cert := c.cert
	return &cert, nil
}

func (c *Controller) pendingRepinPath() string { return filepath.Join(c.dataDir, "tls_repin.json") }

// tlsView describes the certificate in use, for the Settings page.
func (c *Controller) tlsView() map[string]any {
	c.mu.Lock()
	defer c.mu.Unlock()
	v := map[string]any{"fingerprint": c.fingerprint}
	if len(c.cert.Certificate) > 0 {
		if x, err := x509.ParseCertificate(c.cert.Certificate[0]); err == nil {
			v["subject"] = x.Subject.CommonName
			v["issuer"] = x.Issuer.CommonName
			v["self_signed"] = x.Subject.String() == x.Issuer.String()
			v["expires"] = x.NotAfter
			v["names"] = append(append([]string{}, x.DNSNames...), ipStrings(x)...)
		}
	}
	pend := []string{}
	if b, err := os.ReadFile(c.pendingRepinPath()); err == nil {
		var ids []string
		if json.Unmarshal(b, &ids) == nil {
			for _, id := range ids {
				if n := c.nodes[id]; n != nil {
					pend = append(pend, displayName(n))
				}
			}
		}
	}
	v["needs_repin"] = pend
	return v
}

func ipStrings(x *x509.Certificate) []string {
	var out []string
	for _, ip := range x.IPAddresses {
		out = append(out, ip.String())
	}
	return out
}

func (c *Controller) handleTLSInfo(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, 200, c.tlsView())
}

func (c *Controller) handleSetTLS(w http.ResponseWriter, r *http.Request) {
	var req struct{ Cert, Key string }
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&req); err != nil {
		writeJSON(w, 400, map[string]string{"error": "bad json"})
		return
	}
	cert, err := tls.X509KeyPair([]byte(req.Cert), []byte(req.Key))
	if err != nil {
		writeJSON(w, 400, map[string]string{"error": "certificate and key do not match or are not PEM: " + err.Error()})
		return
	}
	leaf, err := x509.ParseCertificate(cert.Certificate[0])
	if err != nil {
		writeJSON(w, 400, map[string]string{"error": err.Error()})
		return
	}
	if time.Now().After(leaf.NotAfter) {
		writeJSON(w, 400, map[string]string{"error": "that certificate has expired"})
		return
	}
	newFP := certFingerprint(cert.Certificate[0])
	if newFP == c.fingerprintNow() {
		writeJSON(w, 200, c.tlsView())
		return
	}

	// 1. Tell connected nodes the new pin and wait for each to confirm.
	c.mu.Lock()
	conns := map[string]*nodeConn{}
	for id, nc := range c.conns {
		conns[id] = nc
	}
	c.repinAcks = map[string]string{}
	c.mu.Unlock()
	for id, nc := range conns {
		if err := nc.send("node.repin", map[string]string{"fingerprint": newFP}); err != nil {
			delete(conns, id)
		}
	}
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		c.mu.Lock()
		done := true
		for id := range conns {
			if c.repinAcks[id] != newFP {
				done = false
			}
		}
		c.mu.Unlock()
		if done {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}

	// 2. Write the files, then switch.
	crt, key := filepath.Join(c.dataDir, "tls.crt"), filepath.Join(c.dataDir, "tls.key")
	// The old pair is kept in memory so a failed second write cannot leave a
	// certificate on disk that does not match its key (the controller would not
	// start again). Reading may fail on a first swap from a generated pair; then
	// there is nothing to put back.
	oldCrt, oldCrtErr := os.ReadFile(crt)
	err = writeFileAtomic(crt, []byte(req.Cert), 0o644)
	if err == nil {
		if err = writeFileAtomic(key, []byte(req.Key), 0o600); err != nil && oldCrtErr == nil {
			_ = writeFileAtomic(crt, oldCrt, 0o644)
		}
	}
	if err != nil {
		// The nodes were already told the new pin. The controller keeps serving the
		// old certificate, so point them back at it or they could not reconnect.
		oldFP := c.fingerprintNow()
		for _, nc := range conns {
			_ = nc.send("node.repin", map[string]string{"fingerprint": oldFP})
		}
		writeJSON(w, 500, map[string]string{"error": "saving the certificate: " + err.Error()})
		return
	}
	var missed []string
	c.mu.Lock()
	for id, n := range c.nodes {
		if c.repinAcks[id] != newFP {
			missed = append(missed, id)
			_ = n
		}
	}
	c.cert, c.fingerprint = cert, newFP
	c.mu.Unlock()
	b, _ := json.Marshal(missed)
	_ = writeFileAtomic(c.pendingRepinPath(), b, 0o600)
	writeJSON(w, 200, c.tlsView())
}

// clearRepin drops a node from the needs-re-pin list once it is connected again.
func (c *Controller) clearRepin(nodeID string) {
	b, err := os.ReadFile(c.pendingRepinPath())
	if err != nil {
		return
	}
	var ids, keep []string
	if json.Unmarshal(b, &ids) != nil {
		return
	}
	for _, id := range ids {
		if id != nodeID {
			keep = append(keep, id)
		}
	}
	if len(keep) != len(ids) {
		nb, _ := json.Marshal(keep)
		_ = writeFileAtomic(c.pendingRepinPath(), nb, 0o600)
	}
}

// repinCommand is printed for nodes that missed the swap.
func repinCommand(fp string) string { return fmt.Sprintf("outpost repin --fingerprint %s", fp) }
