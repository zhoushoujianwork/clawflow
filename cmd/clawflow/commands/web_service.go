package commands

import (
	"bytes"
	"context"
	"encoding/xml"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"github.com/zhoushoujianwork/clawflow/internal/config"
	"github.com/zhoushoujianwork/clawflow/internal/devmode"
)

const webServiceLabel = "com.zhoushoujianwork.clawflow.web"

type webService struct {
	controller                 bool
	home, binary, path, domain string
	run                        func(string, ...string) ([]byte, error)
}

type webServiceStatus struct {
	Loaded   bool
	Running  bool
	PID      int
	LastExit string
}

func newWebService() (webService, error) {
	// The stable LaunchAgent labels belong to the installed host.
	if devmode.Active() {
		return webService{}, fmt.Errorf("%w: launchd services of the installed host are unavailable to a candidate", devmode.ErrBlocked)
	}
	if runtime.GOOS != "darwin" {
		return webService{}, fmt.Errorf("web service is only supported on macOS")
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return webService{}, err
	}
	binary, err := os.Executable()
	if err != nil {
		return webService{}, err
	}
	binary, err = filepath.EvalSymlinks(binary)
	if err != nil {
		return webService{}, err
	}
	return webService{home: home, binary: binary, domain: "gui/" + strconv.Itoa(os.Getuid()),
		path: os.Getenv("PATH"),
		run: func(name string, args ...string) ([]byte, error) {
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()
			return exec.CommandContext(ctx, name, args...).CombinedOutput()
		}}, nil
}

func webServicePATH(current, execution string, lookup func(string) (string, error)) string {
	dirs := []string{}
	seen := map[string]bool{}
	add := func(dir string) {
		if filepath.IsAbs(dir) && !seen[dir] {
			seen[dir] = true
			dirs = append(dirs, dir)
		}
	}
	for _, dir := range filepath.SplitList(current) {
		add(dir)
	}
	if execution == "" {
		execution = "offload"
	}
	for _, name := range []string{execution, "go", "node", "pnpm"} {
		if file, err := lookup(name); err == nil {
			add(filepath.Dir(file))
		}
	}
	for _, dir := range []string{"/usr/bin", "/bin", "/usr/sbin", "/sbin"} {
		add(dir)
	}
	return strings.Join(dirs, string(os.PathListSeparator))
}

func (s webService) label() string {
	if s.controller {
		return "com.zhoushoujianwork.clawflow.controller"
	}
	return webServiceLabel
}
func (s webService) target() string { return s.domain + "/" + s.label() }
func (s webService) plistPath() string {
	return filepath.Join(s.home, "Library", "LaunchAgents", s.label()+".plist")
}
func (s webService) call(name string, args ...string) ([]byte, error) {
	out, err := s.run(name, args...)
	if err != nil {
		return out, fmt.Errorf("%s %s: %w: %s", name, strings.Join(args, " "), err, strings.TrimSpace(string(out)))
	}
	return out, nil
}

func (s webService) status() (webServiceStatus, error) {
	out, err := s.call("launchctl", "print", s.target())
	if err != nil {
		// Only an explicit missing-service response is absence. Permission and
		// transport failures must never authorize stopping a healthy process.
		if strings.Contains(string(out), "Could not find service") {
			return webServiceStatus{LastExit: "unknown"}, nil
		}
		return webServiceStatus{}, err
	}
	result := webServiceStatus{Loaded: true, LastExit: "unknown"}
	depth := 0
	for _, line := range strings.Split(string(out), "\n") {
		line = strings.TrimSpace(line)
		if depth == 1 {
			key, value, ok := strings.Cut(line, " = ")
			if ok {
				switch key {
				case "pid":
					result.PID, _ = strconv.Atoi(value)
				case "state":
					result.Running = value == "running"
				case "last exit code", "last terminating signal":
					result.LastExit = value
				}
			}
		}
		depth += strings.Count(line, "{") - strings.Count(line, "}")
	}
	return result, nil
}

func (s webService) plist(host string, port int) ([]byte, error) {
	if strings.TrimSpace(host) == "" || port < 1 || port > 65535 {
		return nil, fmt.Errorf("host must be nonempty and port must be between 1 and 65535")
	}
	quote := func(value string) string {
		var b bytes.Buffer
		_ = xml.EscapeText(&b, []byte(value))
		return b.String()
	}
	logDir := filepath.Join(s.home, ".clawflow", "logs")
	if info, err := os.Lstat(filepath.Join(s.home, ".clawflow")); err == nil && info.Mode()&os.ModeSymlink != 0 {
		if resolved, err := filepath.EvalSymlinks(logDir); err == nil {
			logDir = resolved
		}
	}
	log := filepath.Join(logDir, "web.launchd.log")
	arguments := fmt.Sprintf("<string>web</string><string>--host</string><string>%s</string><string>--port</string><string>%d</string>", quote(host), port)
	if s.controller {
		arguments = "<string>controller</string>"
		log = filepath.Join(logDir, "controller.launchd.log")
	}
	keepAlive := "<dict><key>SuccessfulExit</key><false/></dict>"
	if s.controller {
		keepAlive = "<true/>"
	}
	return []byte(fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0"><dict>
<key>Label</key><string>%s</string>
<key>ProgramArguments</key><array><string>%s</string>%s</array>
<key>EnvironmentVariables</key><dict><key>HOME</key><string>%s</string><key>PATH</key><string>%s</string></dict>
<key>StandardOutPath</key><string>%s</string><key>StandardErrorPath</key><string>%s</string>
<key>RunAtLoad</key><true/>
<key>KeepAlive</key>%s
<key>ThrottleInterval</key><integer>30</integer>
</dict></plist>
`, s.label(), quote(s.binary), arguments, quote(s.home), quote(s.path), quote(log), quote(log), keepAlive)), nil
}

func (s webService) install(host string, port int) error {
	data, err := s.plist(host, port)
	if err != nil {
		return err
	}
	dest := s.plistPath()
	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Join(s.home, ".clawflow", "logs"), 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(dest), ".web-*.plist")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if _, err := s.call("plutil", "-lint", tmp.Name()); err != nil {
		return err
	}
	status, err := s.status()
	if err != nil {
		return err
	}
	previous, _ := os.ReadFile(dest)
	if status.Loaded && bytes.Equal(previous, data) {
		return nil
	}
	if status.Loaded {
		if _, err := s.call("launchctl", "bootout", s.target()); err != nil {
			return err
		}
	}
	if err := os.Rename(tmp.Name(), dest); err != nil {
		return err
	}
	_, err = s.call("launchctl", "bootstrap", s.domain, dest)
	return err
}

func (s webService) uninstall() error {
	status, err := s.status()
	if err != nil {
		return err
	}
	if status.Loaded {
		if _, err := s.call("launchctl", "bootout", s.target()); err != nil {
			return err
		}
	}
	if err := os.Remove(s.plistPath()); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

func newWebServiceCmd() *cobra.Command {
	return newServiceCmd(false)
}

func newServiceCmd(controller bool) *cobra.Command {
	cmd := &cobra.Command{Use: "service", Short: "Manage the macOS dashboard LaunchAgent"}
	var host string
	var port int
	install := &cobra.Command{Use: "install", Short: "Install and load the dashboard LaunchAgent", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, args []string) error {
		s, err := newWebService()
		if err != nil {
			return err
		}
		s.controller = controller
		cfg, err := config.Load()
		if err != nil {
			return err
		}
		s.path = webServicePATH(os.Getenv("PATH"), cfg.Settings.Execution.Command, exec.LookPath)
		return s.install(host, port)
	}}
	install.Flags().StringVar(&host, "host", "127.0.0.1", "dashboard bind host")
	install.Flags().IntVar(&port, "port", 8090, "dashboard bind port")
	cmd.AddCommand(install, &cobra.Command{Use: "uninstall", Short: "Unload and remove the dashboard LaunchAgent", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, args []string) error {
		s, err := newWebService()
		if err != nil {
			return err
		}
		s.controller = controller
		return s.uninstall()
	}}, &cobra.Command{Use: "status", Short: "Show launchd state, PID and last exit", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, args []string) error {
		s, err := newWebService()
		if err != nil {
			return err
		}
		s.controller = controller
		status, err := s.status()
		if err != nil {
			return err
		}
		fmt.Fprintf(cmd.OutOrStdout(), "loaded=%t running=%t pid=%d last_exit=%s\n", status.Loaded, status.Running, status.PID, status.LastExit)
		return nil
	}})
	return cmd
}

func (s webService) manages(info webPidInfo) (bool, error) {
	if info.ServiceName == webServiceLabel {
		return true, nil
	}
	status, err := s.status()
	return status.Loaded && status.PID == info.PID, err
}

func (s webService) kickstart() error {
	_, err := s.call("launchctl", "kickstart", "-k", s.target())
	return err
}
