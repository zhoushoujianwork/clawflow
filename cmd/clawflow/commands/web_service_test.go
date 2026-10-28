package commands

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestWebServiceInstallLifecycle(t *testing.T) {
	s := webService{home: t.TempDir(), binary: "/Applications/A & B/clawflow", path: "/tools:/usr/bin", domain: "gui/501"}
	loaded := false
	expectedPort := 8090
	var calls []string
	s.run = func(name string, args ...string) ([]byte, error) {
		calls = append(calls, name+" "+strings.Join(args, " "))
		switch name + " " + args[0] {
		case "plutil -lint":
			data, err := os.ReadFile(args[1])
			if err != nil {
				t.Fatal(err)
			}
			for _, want := range []string{"A &amp; B/clawflow", "<string>--host</string><string>127.0.0.1</string>", fmt.Sprintf("<string>--port</string><string>%d</string>", expectedPort), "<key>HOME</key><string>" + s.home, "<key>PATH</key><string>/tools:/usr/bin", "web.launchd.log", "<key>RunAtLoad</key><true/>", "<key>SuccessfulExit</key><false/>", "<key>ThrottleInterval</key><integer>30</integer>"} {
				if !strings.Contains(string(data), want) {
					t.Errorf("plist missing %q", want)
				}
			}
		case "launchctl print":
			if !loaded {
				return []byte("Could not find service"), errors.New("exit 113")
			}
			return []byte("service = {\n state = running\n pid = 123\n last exit code = 1\n}\n"), nil
		case "launchctl bootstrap":
			if args[1] != "gui/501" || args[2] != s.plistPath() {
				t.Fatalf("bootstrap: %v", args)
			}
			loaded = true
		case "launchctl bootout":
			if args[1] != s.target() {
				t.Fatalf("bootout: %v", args)
			}
			loaded = false
		default:
			t.Fatalf("unexpected command %s %v", name, args)
		}
		return nil, nil
	}
	for i := 0; i < 2; i++ {
		if err := s.install("127.0.0.1", 8090); err != nil {
			t.Fatal(err)
		}
	}
	if n := strings.Count(strings.Join(calls, "\n"), "launchctl bootstrap"); n != 1 {
		t.Fatalf("bootstrap count=%d", n)
	}
	status, err := s.status()
	if err != nil || !status.Loaded || !status.Running || status.PID != 123 || status.LastExit != "1" {
		t.Fatalf("status=%+v err=%v", status, err)
	}
	expectedPort = 8091
	calls = nil
	if err := s.install("127.0.0.1", expectedPort); err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(calls, "\n")
	stop, start := strings.Index(joined, "launchctl bootout"), strings.Index(joined, "launchctl bootstrap")
	if stop < 0 || start <= stop {
		t.Fatalf("configuration update did not reload in order: %s", joined)
	}
	calls = nil
	for i := 0; i < 2; i++ {
		if err := s.uninstall(); err != nil {
			t.Fatal(err)
		}
	}
	if n := strings.Count(strings.Join(calls, "\n"), "launchctl bootout"); n != 1 {
		t.Fatalf("bootout count=%d", n)
	}
	if _, err := os.Stat(s.plistPath()); !os.IsNotExist(err) {
		t.Fatalf("plist remains: %v", err)
	}
}

func TestWebServiceValidationPreservesExisting(t *testing.T) {
	for _, fail := range []string{"plutil", "launchctl"} {
		t.Run(fail, func(t *testing.T) {
			s := webService{home: t.TempDir(), binary: "/bin/clawflow", domain: "gui/501"}
			if err := os.MkdirAll(filepath.Dir(s.plistPath()), 0755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(s.plistPath(), []byte("existing"), 0600); err != nil {
				t.Fatal(err)
			}
			s.run = func(name string, args ...string) ([]byte, error) {
				if args[0] == "bootout" || args[0] == "bootstrap" {
					t.Fatal("mutated service after validation failure")
				}
				if name == fail {
					return []byte("permission denied"), errors.New("failed")
				}
				return nil, nil
			}
			if err := s.install("localhost", 8090); err == nil {
				t.Fatal("expected failure")
			}
			data, _ := os.ReadFile(s.plistPath())
			if string(data) != "existing" {
				t.Fatal("overwrote healthy config")
			}
		})
	}
	s := webService{}
	for _, port := range []int{0, -1, 65536} {
		if _, err := s.plist("localhost", port); err == nil {
			t.Fatal("invalid port accepted")
		}
	}
	if _, err := s.plist("", 8090); err == nil {
		t.Fatal("empty host accepted")
	}
}

func TestWebServicePATH(t *testing.T) {
	got := webServicePATH("/usr/bin:/tools:.:/tools", "/custom/offload", func(name string) (string, error) {
		switch name {
		case "/custom/offload":
			return name, nil
		case "go":
			return "/sdk/go/bin/go", nil
		case "node":
			return "/node/bin/node", nil
		case "pnpm":
			return "/pnpm/bin/pnpm", nil
		}
		return "", errors.New("missing")
	})
	if got != "/usr/bin:/tools:/custom:/sdk/go/bin:/node/bin:/pnpm/bin:/bin:/usr/sbin:/sbin" {
		t.Fatal(got)
	}
}

func TestWebServiceOwnership(t *testing.T) {
	for _, tc := range []struct {
		name   string
		record string
		pid    int
		want   bool
	}{
		{"environment", webServiceLabel, 42, true}, {"legacy pid", "", 42, true}, {"unrelated", "", 43, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := webService{run: func(string, ...string) ([]byte, error) {
				if tc.record != "" {
					t.Fatal("environment should suffice")
				}
				return []byte("service = {\n pid = 42\n nested = {\n pid = 43\n }\n}\n"), nil
			}}
			got, err := s.manages(webPidInfo{PID: tc.pid, ServiceName: tc.record})
			if err != nil || got != tc.want {
				t.Fatalf("managed=%t err=%v", got, err)
			}
		})
	}
}

func TestWebRestartRoutingAndWait(t *testing.T) {
	for _, tc := range []struct {
		name    string
		managed bool
		failure string
	}{
		{"launchd", true, ""}, {"manual", false, ""}, {"detection failure", false, "detect"}, {"kickstart failure", true, "kick"}, {"launchd timeout", true, "timeout"}, {"manual busy port", false, "timeout"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			now := time.Unix(0, 0)
			signals, kicks, execs, probes := 0, 0, 0, 0
			ops := webRestartOps{
				managed: func(webPidInfo) (bool, error) {
					if tc.failure == "detect" {
						return false, errors.New("denied")
					}
					return tc.managed, nil
				},
				kickstart: func() error {
					kicks++
					if tc.failure == "kick" {
						return errors.New("failed")
					}
					return nil
				},
				signal: func(int) error { signals++; return nil },
				alive:  func(int) bool { return now.Before(time.Unix(0, 0).Add(200 * time.Millisecond)) },
				exec: func(info webPidInfo) error {
					execs++
					if info.Host != "0.0.0.0" || info.Port != 8090 || info.Remote != "readonly" || len(info.AllowCIDRs) != 1 {
						t.Fatal("lost bind flags")
					}
					return nil
				},
				ready: func(addr string) bool {
					probes++
					if addr != "127.0.0.1:8090" {
						t.Fatal(addr)
					}
					return tc.failure != "timeout" && probes > 1
				},
				free: func(string) bool { probes++; return tc.failure != "timeout" && probes > 1 },
				now:  func() time.Time { return now }, sleep: func(d time.Duration) { now = now.Add(d) },
			}
			err := restartWeb(webPidInfo{PID: 42, Host: "0.0.0.0", Port: 8090, Remote: "readonly", AllowCIDRs: []string{"192.168.0.0/16"}}, time.Second, ops)
			if (err != nil) != (tc.failure != "") {
				t.Fatalf("err=%v", err)
			}
			if tc.managed && signals != 0 {
				t.Fatal("signalled launchd process")
			}
			if tc.failure == "detect" && (signals+kicks+execs) != 0 {
				t.Fatal("detection failure mutated process")
			}
			if tc.failure != "" && execs != 0 {
				t.Fatal("spawned on failure")
			}
			if tc.failure == "" {
				if probes < 2 {
					t.Fatal("did not wait for port")
				}
				if tc.managed && kicks != 1 || !tc.managed && (signals != 1 || execs != 1) {
					t.Fatal(fmt.Sprint(signals, kicks, execs))
				}
			}
		})
	}
}

func TestWebServiceUninstallFailurePreservesPlist(t *testing.T) {
	s := webService{home: t.TempDir()}
	if err := os.MkdirAll(filepath.Dir(s.plistPath()), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(s.plistPath(), []byte("existing"), 0600); err != nil {
		t.Fatal(err)
	}
	s.run = func(_ string, args ...string) ([]byte, error) {
		if args[0] == "print" {
			return []byte("service = {\n state = running\n pid = 42\n}\n"), nil
		}
		return nil, errors.New("bootout denied")
	}
	if err := s.uninstall(); err == nil {
		t.Fatal("expected failure")
	}
	if _, err := os.Stat(s.plistPath()); err != nil {
		t.Fatal("removed plist on failure")
	}
}

func TestWebServicePlutil(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("plutil requires macOS")
	}
	s := webService{home: t.TempDir(), binary: "/some/A & B/clawflow", path: "/bin:/custom"}
	data, err := s.plist("::1", 8090)
	if err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(t.TempDir(), "web.plist")
	if err := os.WriteFile(file, data, 0600); err != nil {
		t.Fatal(err)
	}
	if out, err := exec.Command("plutil", "-lint", file).CombinedOutput(); err != nil {
		t.Fatalf("plutil: %v: %s", err, out)
	}
}

func TestWebServiceKickstart(t *testing.T) {
	s := webService{domain: "gui/501", run: func(name string, args ...string) ([]byte, error) {
		if got := name + " " + strings.Join(args, " "); got != "launchctl kickstart -k gui/501/"+webServiceLabel {
			t.Fatal(got)
		}
		return nil, errors.New("denied")
	}}
	if err := s.kickstart(); err == nil {
		t.Fatal("lost launchctl failure")
	}
}

func TestWebServicePidRecord(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	want := webPidInfo{PID: 42, Host: "127.0.0.1", Port: 8090, ServiceName: webServiceLabel, Remote: "readonly", AllowCIDRs: []string{"192.168.0.0/16"}}
	if err := writeWebPid(want); err != nil {
		t.Fatal(err)
	}
	if got, ok := readWebPid(); !ok || !reflect.DeepEqual(got, want) {
		t.Fatalf("pid record=%+v valid=%t", got, ok)
	}
	if err := os.WriteFile(webPidPath(), []byte(`{"pid":42,"host":"localhost","port":8090}`), 0600); err != nil {
		t.Fatal(err)
	}
	if got, ok := readWebPid(); !ok || got.ServiceName != "" {
		t.Fatalf("legacy pid record=%+v valid=%t", got, ok)
	}
}

func TestWebRestartRefusesInvalidWait(t *testing.T) {
	for _, wait := range []time.Duration{0, -time.Second} {
		// Empty operations panic if validation touches any process.
		if err := restartWeb(webPidInfo{PID: 42}, wait, webRestartOps{}); err == nil {
			t.Fatal("invalid wait accepted")
		}
	}
}

func TestWebRestartDoesNotAcceptOldListener(t *testing.T) {
	for _, managed := range []bool{false, true} {
		now := time.Unix(0, 0)
		ops := webRestartOps{
			managed:   func(webPidInfo) (bool, error) { return managed, nil },
			kickstart: func() error { return nil },
			signal:    func(int) error { return nil },
			alive:     func(int) bool { return true },
			ready:     func(string) bool { t.Fatal("probed old listener"); return true },
			free:      func(string) bool { t.Fatal("probed before old process exited"); return true },
			exec:      func(webPidInfo) error { t.Fatal("started duplicate instance"); return nil },
			now:       func() time.Time { return now },
			sleep:     func(d time.Duration) { now = now.Add(d) },
		}
		if err := restartWeb(webPidInfo{PID: 42, Host: "localhost", Port: 8090}, time.Second, ops); err == nil {
			t.Fatal("reported success while old process remained alive")
		}
	}
}

func TestWebRestartArguments(t *testing.T) {
	info := webPidInfo{Host: "::", Port: 8090, Remote: "readonly", AllowCIDRs: []string{"192.168.0.0/16", "fd00::/8"}}
	want := []string{"clawflow", "web", "--host", "::", "--port", "8090", "--remote", "readonly", "--allow-cidr", "192.168.0.0/16", "--allow-cidr", "fd00::/8"}
	if got := webArgs(info); !reflect.DeepEqual(got, want) {
		t.Fatalf("args=%v want=%v", got, want)
	}
	if got := remoteArgs("", nil); !reflect.DeepEqual(got, []string{"--remote", "off"}) {
		t.Fatalf("legacy=%v", got)
	}
}
