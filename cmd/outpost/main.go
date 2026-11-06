package main

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"encoding/pem"
	"errors"
	"fmt"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

func usage() {
	fmt.Fprintf(os.Stderr, `Outpost %s — self-hosted AI cluster: controller and node agent

Set up as a service that starts at boot (Linux, systemd; run with sudo):
  outpost install controller [--advertise https://IP:8443] [--listen :8443] [--data DIR]
  outpost install node --join TOKEN [--user USER] [--data DIR] [--name NAME]
  outpost install node                  (machine already joined; reuse its config)
  outpost uninstall controller|node     (removes the service, keeps data)
  outpost reset-password                (set a password, or clear it to choose in the web UI; restarts the service)
  outpost install controller --restore backup.opb    (rebuild a controller from a backup)

Run in the foreground:
  outpost controller [--listen :8443] [--data DIR] [--advertise https://IP:8443]
  outpost node --join TOKEN [--data DIR] [--name NAME]   (first run)
  outpost node [--data DIR]                              (later runs)

  outpost version
  outpost tray [--data DIR]              (status icon: pause work, join another controller, quit)
  outpost repin --fingerprint SHA256 [--data DIR]
                                        (node: trust the controller's new TLS certificate)
  outpost licenses                      (show the AGPL-3.0 licence, source link and third-party notices)
  outpost hwinfo                        (show the CPU, RAM and GPUs this machine reports)
`, Version)
}

func versionString() string {
	return "Outpost " + strings.Split(buildInfo, "|")[1] + " " + runtime.GOOS + "/" + runtime.GOARCH
}

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}
	logToFileFromEnv()
	var err error
	switch os.Args[1] {
	case "controller":
		err = runController(os.Args[2:])
	case "node":
		err = runNode(os.Args[2:])
	case "install":
		err = runInstall(os.Args[2:])
	case "uninstall":
		err = runUninstall(os.Args[2:])
	case "reset-password":
		err = runResetPassword(os.Args[2:])
	case "sandbox-child":
		runSandboxChild(os.Args[2:])
	case "tray":
		err = runTray(os.Args[2:])
	case "service":
		err = runService(os.Args[2:])
	case "repin":
		err = runRepin(os.Args[2:])
	case "licenses":
		runLicenses()
	case "catalog-json":
		runCatalogJSON()
	case "hwinfo":
		runHWInfo()
	case "version", "--version", "-v":
		fmt.Println(versionString())
	default:
		usage()
		os.Exit(2)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

func defaultDataDir(mode string) string {
	switch runtime.GOOS {
	case "windows":
		if pd := os.Getenv("ProgramData"); pd != "" {
			return filepath.Join(pd, "outpost", mode)
		}
	case "darwin":
		if os.Geteuid() == 0 {
			return filepath.Join("/Library/Application Support/outpost", mode)
		}
	default:
		if os.Geteuid() == 0 {
			return filepath.Join("/var/lib/outpost", mode)
		}
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".outpost", mode)
}

// ---- TLS ----

func certFingerprint(der []byte) string {
	s := sha256.Sum256(der)
	return hex.EncodeToString(s[:])
}

// loadOrCreateCert keeps a self-signed ECDSA cert in dir. Nodes pin its SHA-256
// fingerprint, so no CA is needed.
func loadOrCreateCert(dir string) (tls.Certificate, string, error) {
	certPath, keyPath := filepath.Join(dir, "tls.crt"), filepath.Join(dir, "tls.key")
	if c, err := tls.LoadX509KeyPair(certPath, keyPath); err == nil {
		return c, certFingerprint(c.Certificate[0]), nil
	}
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return tls.Certificate{}, "", err
	}
	host, _ := os.Hostname()
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(time.Now().UnixNano()),
		Subject:      pkix.Name{CommonName: "Outpost controller " + host},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().AddDate(10, 0, 0),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		DNSNames:     []string{"localhost", host},
	}
	for _, ip := range localIPs() {
		tmpl.IPAddresses = append(tmpl.IPAddresses, ip)
	}
	tmpl.IPAddresses = append(tmpl.IPAddresses, net.ParseIP("127.0.0.1"))
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		return tls.Certificate{}, "", err
	}
	kb, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		return tls.Certificate{}, "", err
	}
	if err := os.WriteFile(certPath, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0o644); err != nil {
		return tls.Certificate{}, "", err
	}
	if err := os.WriteFile(keyPath, pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: kb}), 0o600); err != nil {
		return tls.Certificate{}, "", err
	}
	c, err := tls.LoadX509KeyPair(certPath, keyPath)
	return c, certFingerprint(der), err
}

// pinnedTLS trusts exactly one certificate, identified by fingerprint.
func pinnedTLS(fingerprint string) *tls.Config {
	want := strings.ToLower(fingerprint)
	return &tls.Config{
		InsecureSkipVerify: true, // replaced by the pin check below
		VerifyPeerCertificate: func(raw [][]byte, _ [][]*x509.Certificate) error {
			if len(raw) == 0 {
				return errors.New("no server certificate")
			}
			if certFingerprint(raw[0]) != want {
				return fmt.Errorf("controller certificate fingerprint mismatch (got %s); if you replaced the controller's certificate, run: outpost repin --fingerprint %s", certFingerprint(raw[0]), certFingerprint(raw[0]))
			}
			return nil
		},
		MinVersion: tls.VersionTLS12,
	}
}

func localIPs() []net.IP {
	var out []net.IP
	ifaces, _ := net.Interfaces()
	for _, ifc := range ifaces {
		if ifc.Flags&net.FlagUp == 0 || ifc.Flags&net.FlagLoopback != 0 {
			continue
		}
		addrs, _ := ifc.Addrs()
		for _, a := range addrs {
			if ipn, ok := a.(*net.IPNet); ok && ipn.IP.To4() != nil {
				out = append(out, ipn.IP)
			}
		}
	}
	return out
}
