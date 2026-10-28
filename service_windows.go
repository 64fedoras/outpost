//go:build windows

package main

import (
	"errors"
	"flag"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/svc"
	"golang.org/x/sys/windows/svc/mgr"
)

// The "Outpost Node" Windows service (0.7). The service process is only a
// launcher: it runs `outpost node` as a child (the newest installed copy,
// the same handover as the per-user launcher), restarts it after an update
// (exit code relaunchExitCode), and stops the service when the child exits
// with exitQuitService (Quit from the tray icon). The child runs inside a job
// object, so llama-server and any running commands die with it.

const (
	serviceName    = "OutpostNode"
	serviceDisplay = "Outpost Node"
)

// runService is `outpost service --data DIR`, started by the service manager.
func runService(args []string) error {
	fsf := flag.NewFlagSet("service", flag.ExitOnError)
	data := fsf.String("data", defaultDataDir("node"), "the node's data directory")
	_ = fsf.Parse(args)
	if ok, _ := svc.IsWindowsService(); !ok {
		return errors.New("`outpost service` is started by Windows; to install it run `outpost install node` from an Administrator PowerShell")
	}
	if lf, err := openNodeLog(*data); err == nil {
		log.SetOutput(lf)
	}
	return svc.Run(serviceName, &nodeService{data: *data})
}

type nodeService struct{ data string }

func (s *nodeService) Execute(_ []string, req <-chan svc.ChangeRequest, st chan<- svc.Status) (bool, uint32) {
	st <- svc.Status{State: svc.StartPending}
	stop := make(chan struct{})
	done := make(chan struct{})
	go func() { s.loop(stop); close(done) }()
	st <- svc.Status{State: svc.Running, Accepts: svc.AcceptStop | svc.AcceptShutdown}
	for {
		select {
		case <-done: // the child asked to quit
			st <- svc.Status{State: svc.StopPending}
			return false, 0
		case c := <-req:
			switch c.Cmd {
			case svc.Interrogate:
				st <- c.CurrentStatus
			case svc.Stop, svc.Shutdown:
				st <- svc.Status{State: svc.StopPending}
				close(stop)
				select {
				case <-done:
				case <-time.After(20 * time.Second):
				}
				return false, 0
			}
		}
	}
}

func (s *nodeService) loop(stop <-chan struct{}) {
	agentDir := filepath.Join(s.data, "agent")
	logf, _ := openNodeLog(s.data)
	for {
		path, _ := os.Executable()
		if c, ok := newestCopy(agentDir); ok {
			path = c.path
		}
		log.Printf("service: starting %s node", path)
		cmd := exec.Command(path, "node", "--data", s.data)
		cmd.Stdout, cmd.Stderr = logf, logf
		cmd.Env = append(os.Environ(), "OUTPOST_CHILD=1", "OUTPOST_SERVICE=1")
		cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: createNoWindow}
		job := newKillOnCloseJob()
		if err := cmd.Start(); err != nil {
			log.Printf("service: starting the node: %v", err)
		} else {
			job.add(cmd)
			exited := make(chan error, 1)
			go func() { exited <- cmd.Wait() }()
			select {
			case <-stop:
				job.close() // kills the node and everything it started
				<-exited
				return
			case err := <-exited:
				job.close()
				var ee *exec.ExitError
				if errors.As(err, &ee) && ee.ExitCode() == exitQuitService {
					return
				}
				if errors.As(err, &ee) && ee.ExitCode() == relaunchExitCode {
					continue // an update was installed; start the newest copy
				}
				log.Printf("service: node exited: %v", err)
			}
		}
		select {
		case <-stop:
			return
		case <-time.After(5 * time.Second):
		}
	}
}

// ---- job object: closing it kills the node and its children ----

type killJob struct{ h windows.Handle }

func newKillOnCloseJob() *killJob {
	h, err := windows.CreateJobObject(nil, nil)
	if err != nil {
		return &killJob{}
	}
	var info windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION
	info.BasicLimitInformation.LimitFlags = windows.JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE
	if _, err := windows.SetInformationJobObject(h, windows.JobObjectExtendedLimitInformation, uintptr(unsafe.Pointer(&info)), uint32(unsafe.Sizeof(info))); err != nil {
		windows.CloseHandle(h)
		return &killJob{}
	}
	return &killJob{h: h}
}

func (j *killJob) add(cmd *exec.Cmd) {
	if j.h == 0 {
		return
	}
	ph, err := windows.OpenProcess(windows.PROCESS_SET_QUOTA|windows.PROCESS_TERMINATE, false, uint32(cmd.Process.Pid))
	if err != nil {
		return
	}
	defer windows.CloseHandle(ph)
	_ = windows.AssignProcessToJobObject(j.h, ph)
}

func (j *killJob) close() {
	if j.h != 0 {
		windows.CloseHandle(j.h)
		j.h = 0
	}
}

// ---- install / uninstall ----

func isElevated() bool { return windows.GetCurrentProcessToken().IsElevated() }

// installService registers (or updates) the service and starts it.
func installService(exe, data string) error {
	m, err := mgr.Connect()
	if err != nil {
		return fmt.Errorf("opening the service manager: %w", err)
	}
	defer m.Disconnect()
	cfg := mgr.Config{DisplayName: serviceDisplay, StartType: mgr.StartAutomatic,
		Description: "Outpost node: runs local AI models and jobs for the Outpost controller."}
	s, err := m.OpenService(serviceName)
	if err == nil {
		stopService(s)
		cur, cerr := s.Config()
		if cerr == nil {
			cur.BinaryPathName = fmt.Sprintf(`"%s" service --data "%s"`, exe, data)
			cur.DisplayName, cur.StartType, cur.Description = cfg.DisplayName, cfg.StartType, cfg.Description
			err = s.UpdateConfig(cur)
		} else {
			err = cerr
		}
	} else {
		s, err = m.CreateService(serviceName, exe, cfg, "service", "--data", data)
	}
	if err != nil {
		return fmt.Errorf("registering the service: %w", err)
	}
	defer s.Close()
	_ = s.SetRecoveryActions([]mgr.RecoveryAction{
		{Type: mgr.ServiceRestart, Delay: 5 * time.Second},
		{Type: mgr.ServiceRestart, Delay: 30 * time.Second},
		{Type: mgr.ServiceRestart, Delay: time.Minute},
	}, 86400)
	return s.Start()
}

func stopService(s *mgr.Service) {
	if st, err := s.Control(svc.Stop); err == nil {
		for i := 0; i < 40 && st.State != svc.Stopped; i++ {
			time.Sleep(250 * time.Millisecond)
			if st, err = s.Query(); err != nil {
				return
			}
		}
	}
}

// serviceInstalled reports whether the service exists, and stops it if running.
func stopInstalledService() bool {
	m, err := mgr.Connect()
	if err != nil {
		return false
	}
	defer m.Disconnect()
	s, err := m.OpenService(serviceName)
	if err != nil {
		return false
	}
	defer s.Close()
	stopService(s)
	return true
}

func uninstallNodeWindows() error {
	if !isElevated() {
		return errors.New("run this from an Administrator PowerShell")
	}
	m, err := mgr.Connect()
	if err != nil {
		return err
	}
	defer m.Disconnect()
	if s, err := m.OpenService(serviceName); err == nil {
		stopService(s)
		err = s.Delete()
		s.Close()
		if err != nil {
			return err
		}
		fmt.Println("Removed the Outpost Node service.")
	} else {
		fmt.Println("The Outpost Node service is not installed.")
	}
	removeRunKey()
	fmt.Println("The data folder and models were kept.")
	return nil
}

// addDefenderExclusion excludes the Outpost folder from Microsoft Defender
// scans so web-UI updates are not blocked. Failure is not fatal.
func addDefenderExclusion(dir string) {
	fmt.Printf("Adding a Microsoft Defender exclusion for %s.\n"+
		"  Why: Defender sometimes blocks or deletes a freshly downloaded, unsigned exe during an update.\n"+
		"  Undo: Remove-MpPreference -ExclusionPath '%s'\n", dir, dir)
	out, err := exec.Command("powershell", "-NoProfile", "-NonInteractive", "-Command",
		fmt.Sprintf("Add-MpPreference -ExclusionPath '%s'", dir)).CombinedOutput()
	if err != nil {
		fmt.Printf("  Could not add it (%v). If updates get blocked, run in an admin PowerShell:\n  Add-MpPreference -ExclusionPath '%s'\n", err, dir)
		_ = out
	}
}
