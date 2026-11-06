package main

// Wake-on-LAN (idea 29): nodes report their network cards' MAC addresses in
// the hello, and the controller can send a magic packet to a node that is
// offline: from its card (Wake) or, with the setting on, when a task is
// waiting for a worker. Needs Wake-on-LAN turned on in the machine's firmware
// and OS, and the controller on the same network segment (broadcasts do not
// cross routers).

import (
	"errors"
	"log"
	"net"
	"net/http"
	"sort"
	"strings"
	"time"
)

// wakeMACs lists this machine's MAC addresses for interfaces that are up,
// not loopback, and have an IPv4 address (the ones Wake-on-LAN can use).
func wakeMACs() []string {
	ifs, err := net.Interfaces()
	if err != nil {
		return nil
	}
	var out []string
	for _, ifc := range ifs {
		if ifc.Flags&net.FlagUp == 0 || ifc.Flags&net.FlagLoopback != 0 || len(ifc.HardwareAddr) != 6 {
			continue
		}
		addrs, _ := ifc.Addrs()
		v4 := false
		for _, a := range addrs {
			if ipn, ok := a.(*net.IPNet); ok && ipn.IP.To4() != nil {
				v4 = true
			}
		}
		if v4 {
			out = append(out, ifc.HardwareAddr.String())
		}
	}
	sort.Strings(out)
	return out
}

// magicPacket is 6 bytes of 0xFF and the MAC 16 times.
func magicPacket(mac string) ([]byte, error) {
	hw, err := net.ParseMAC(mac)
	if err != nil || len(hw) != 6 {
		return nil, errors.New("not a MAC address: " + mac)
	}
	p := make([]byte, 0, 102)
	for i := 0; i < 6; i++ {
		p = append(p, 0xFF)
	}
	for i := 0; i < 16; i++ {
		p = append(p, hw...)
	}
	return p, nil
}

// wakeTargets are where the packet goes: the limited broadcast, and the /24
// broadcast of the address the node last connected from (helps when the
// controller has several networks).
func wakeTargets(lastAddr string) []string {
	out := []string{"255.255.255.255:9"}
	host, _, err := net.SplitHostPort(lastAddr)
	if err != nil {
		host = lastAddr
	}
	if ip := net.ParseIP(host).To4(); ip != nil && !ip.IsLoopback() {
		out = append(out, net.IPv4(ip[0], ip[1], ip[2], 255).String()+":9")
	}
	return out
}

// sendWake is replaceable in tests.
var sendWake = func(target string, pkt []byte) error {
	conn, err := net.Dial("udp4", target)
	if err != nil {
		return err
	}
	defer conn.Close()
	_, err = conn.Write(pkt)
	return err
}

// wakeNode sends magic packets for every MAC the node reported.
func (c *Controller) wakeNode(id string) error {
	c.mu.Lock()
	n := c.nodes[id]
	if n == nil {
		c.mu.Unlock()
		return errors.New("no such node")
	}
	var macs []string
	if n.Hello != nil {
		macs = n.Hello.MACs
	}
	addr := n.LastAddr
	_, online := c.conns[id]
	c.mu.Unlock()
	if online {
		return errors.New("the node is already online")
	}
	if len(macs) == 0 {
		return errors.New("this node has not reported a network card yet (it needs this version, connected once)")
	}
	var errs []string
	sent := 0
	for _, mac := range macs {
		pkt, err := magicPacket(mac)
		if err != nil {
			errs = append(errs, err.Error())
			continue
		}
		for _, t := range wakeTargets(addr) {
			if err := sendWake(t, pkt); err != nil {
				errs = append(errs, t+": "+err.Error())
				continue
			}
			sent++
		}
	}
	if sent == 0 {
		return errors.New("could not send the wake packet: " + strings.Join(errs, "; "))
	}
	c.mu.Lock()
	if c.woken == nil {
		c.woken = map[string]time.Time{}
	}
	c.woken[id] = time.Now()
	c.mu.Unlock()
	return nil
}

const wakeEvery = 10 * time.Minute

// wakeForWork wakes offline nodes that could take work (inside their hours,
// carrying label if one is asked for, with a known MAC) when the setting is
// on. Each node is woken at most once per wakeEvery.
func (c *Controller) wakeForWork(label string) {
	c.mu.Lock()
	if !c.settings.WakeForWork {
		c.mu.Unlock()
		return
	}
	now := time.Now()
	var ids []string
	for id, n := range c.nodes {
		if _, on := c.conns[id]; on || n.Hello == nil || len(n.Hello.MACs) == 0 || !n.Hours.open(now) {
			continue
		}
		if label != "" && !hasLabel(n.Labels, label) {
			continue
		}
		if t, ok := c.woken[id]; ok && now.Sub(t) < wakeEvery {
			continue
		}
		ids = append(ids, id)
	}
	c.mu.Unlock()
	for _, id := range ids {
		if err := c.wakeNode(id); err == nil {
			log.Printf("sent a Wake-on-LAN packet to node %s: a task is waiting for a worker", id)
		}
	}
}

func (c *Controller) handleWakeNode(w http.ResponseWriter, r *http.Request) {
	if err := c.wakeNode(r.PathValue("id")); err != nil {
		writeJSON(w, 409, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, 200, map[string]bool{"ok": true})
}
