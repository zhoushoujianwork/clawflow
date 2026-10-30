package commands

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/tls"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
	"github.com/zhoushoujianwork/clawflow/internal/config"
	"github.com/zhoushoujianwork/clawflow/internal/coordinator"
	"github.com/zhoushoujianwork/clawflow/internal/housekeeping"
)

func NewPlatformCmd() *cobra.Command {
	cmd := &cobra.Command{Use: "platform", Short: "连接和查看 ClawFlow 平台"}
	var settings config.PlatformSettings
	login := &cobra.Command{Use: "login", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		// Verify credentials and trust before persisting a new destination.
		if err := printPlatformStatus(cmd, settings); err != nil {
			return err
		}
		cfg, err := loadOrNewConfig()
		if err != nil {
			return err
		}
		path, err := filepath.Abs(settings.TokenFile)
		if err != nil {
			return err
		}
		settings.TokenFile = path
		settings.URL = strings.TrimSuffix(settings.URL, "/")
		cfg.Settings.Platform = settings
		return cfg.Save()
	}}
	login.Flags().StringVar(&settings.URL, "url", "", "平台 HTTPS 地址")
	login.Flags().StringVar(&settings.TokenFile, "token-file", "", "本机 admin token 文件")
	login.Flags().StringVar(&settings.Fingerprint, "fingerprint", "", "自签名平台的 SHA-256 证书指纹（可选）")
	_ = login.MarkFlagRequired("url")
	_ = login.MarkFlagRequired("token-file")
	cmd.AddCommand(login, &cobra.Command{Use: "status", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		cfg, err := config.Load()
		if err != nil {
			return err
		}
		return printPlatformStatus(cmd, cfg.Settings.Platform)
	}})
	return cmd
}

func platformCall(ctx context.Context, settings config.PlatformSettings, method, path string, input, output any) error {
	u, err := url.Parse(settings.URL)
	if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Path != "" && u.Path != "/") {
		return fmt.Errorf("平台必须为 HTTPS 源地址")
	}
	tlsConfig := &tls.Config{MinVersion: tls.VersionTLS12}
	if settings.Fingerprint != "" {
		pin, err := hex.DecodeString(strings.ReplaceAll(settings.Fingerprint, ":", ""))
		if err != nil || len(pin) != sha256.Size {
			return fmt.Errorf("无效的平台证书指纹")
		}
		tlsConfig.InsecureSkipVerify = true // Trust the explicitly configured pin instead of system CAs.
		tlsConfig.VerifyConnection = func(cs tls.ConnectionState) error {
			if len(cs.PeerCertificates) == 0 {
				return fmt.Errorf("平台证书缺失")
			}
			sum := sha256.Sum256(cs.PeerCertificates[0].Raw)
			if !bytes.Equal(sum[:], pin) {
				return fmt.Errorf("平台证书指纹不匹配")
			}
			return nil
		}
	}
	token, err := os.ReadFile(settings.TokenFile)
	if err != nil {
		return fmt.Errorf("无法读取平台令牌文件")
	}
	secret := strings.TrimSpace(string(token))
	if secret == "" || strings.ContainsAny(secret, "\r\n") {
		return fmt.Errorf("平台令牌文件无效")
	}
	var body io.Reader
	if input != nil {
		data, err := json.Marshal(input)
		if err != nil {
			return err
		}
		body = bytes.NewReader(data)
	}
	req, err := http.NewRequestWithContext(ctx, method, strings.TrimSuffix(settings.URL, "/")+path, body)
	if err != nil {
		return fmt.Errorf("无效的平台请求")
	}
	req.Header.Set("Authorization", "Bearer "+secret)
	req.Header.Set("Content-Type", "application/json")
	transport := &http.Transport{TLSClientConfig: tlsConfig}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport, Timeout: 15 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("平台请求失败（请检查地址、TLS 信任和连接）")
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return fmt.Errorf("平台返回 HTTP %d", resp.StatusCode)
	}
	return json.NewDecoder(io.LimitReader(resp.Body, 8<<20)).Decode(output)
}

func printPlatformStatus(cmd *cobra.Command, s config.PlatformSettings) error {
	var status coordinator.PlatformStatus
	if err := platformCall(cmd.Context(), s, "GET", "/admin/v1/status", nil, &status); err != nil {
		return err
	}
	fmt.Fprintf(cmd.OutOrStdout(), "平台: %s\n版本: %s\n", s.URL, status.Version)
	return json.NewEncoder(cmd.OutOrStdout()).Encode(status)
}

func routePlatformRepo(cmd *cobra.Command) {
	local := cmd.RunE
	cmd.RunE = func(cmd *cobra.Command, args []string) error {
		cfg, err := loadOrNewConfig()
		if err != nil {
			return err
		}
		s := cfg.Settings.Platform
		if s.URL == "" {
			return local(cmd, args)
		}
		input := coordinator.RepoRequest{Action: cmd.Name(), Options: map[string]string{}}
		if len(args) > 0 {
			input.Repo = args[0]
		}
		cmd.Flags().Visit(func(f *pflag.Flag) {
			if f.Name != "debug" {
				input.Options[f.Name] = f.Value.String()
			}
		})
		if _, ok := input.Options["local-path"]; ok {
			return fmt.Errorf("平台注册无需本机 checkout；请移除 --local-path")
		}
		method := "POST"
		if input.Action == "list" {
			method = "GET"
		}
		var out any
		if err := platformCall(cmd.Context(), s, method, "/admin/v1/repos", input, &out); err != nil {
			return err
		}
		fmt.Fprintf(cmd.OutOrStdout(), "平台: %s\n", s.URL)
		return json.NewEncoder(cmd.OutOrStdout()).Encode(out)
	}
}

// platformAdmin invokes the same local commands, never their client wrappers.
// Only allowlisted flags reach Cobra; local paths and label writes are excluded.
type platformAdmin struct {
	mu      sync.Mutex
	board   func(context.Context) (coordinator.Board, error)
	workers *coordinator.Store
}

func (a *platformAdmin) Repos(ctx context.Context, input coordinator.RepoRequest) (any, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if input.Action == "list" {
		cfg, err := config.Load()
		if err != nil {
			return nil, err
		}
		repos := map[string]config.Repo{}
		for name, r := range cfg.Repos {
			r.LocalPath = ""
			repos[name] = r
		}
		return repos, nil
	}
	var cmd *cobra.Command
	switch input.Action {
	case "add":
		cmd = newRepoAddCmd()
	case "enable":
		cmd = newRepoEnableCmd()
	case "disable":
		cmd = newRepoDisableCmd()
	case "set":
		cmd = newRepoSetCmd()
	default:
		return nil, coordinator.ErrInvalid
	}
	// Refuse filesystem inputs (including malformed SSH URLs) before the parser.
	repo := strings.TrimSpace(input.Repo)
	if repo == "" || strings.HasPrefix(repo, "-") || strings.HasPrefix(repo, ".") || strings.HasPrefix(repo, "/") || strings.HasPrefix(repo, "~") || strings.ContainsAny(repo, "\r\n\t") {
		return nil, coordinator.ErrInvalid
	}
	if strings.HasPrefix(repo, "git@") {
		parts := strings.SplitN(repo, ":", 2)
		if len(parts) != 2 || !strings.Contains(parts[1], "/") {
			return nil, coordinator.ErrInvalid
		}
	}
	args := []string{repo}
	for name, value := range input.Options {
		if name == "local-path" || name == "help" || cmd.Flags().Lookup(name) == nil {
			return nil, coordinator.ErrInvalid
		}
		args = append(args, "--"+name+"="+value)
	}
	if input.Action == "add" {
		args = append(args, "--skip-label-init=true")
	}
	cmd.SetArgs(args)
	cmd.SetContext(ctx)
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	cmd.SilenceErrors = true
	cmd.SilenceUsage = true
	if err := cmd.Execute(); err != nil {
		return nil, err
	}
	return map[string]bool{"ok": true}, nil
}

func (a *platformAdmin) Status(ctx context.Context) (coordinator.PlatformStatus, error) {
	board, err := a.board(ctx)
	out := coordinator.PlatformStatus{Version: Version, Queue: map[string]int{}, Workers: []coordinator.BoardWorker{}}
	limits := (config.HousekeepingSettings{}).Limits()
	if cfg, err := config.Load(); err == nil {
		limits = cfg.Settings.Housekeeping.Limits()
	}
	if disk, err := housekeeping.Probe(config.StateDir(), limits.DiskWarningPercent); err == nil {
		out.Disk = &disk
	} else {
		out.DiskError = "host disk probe unavailable"
	}
	if disk, err := housekeeping.Probe(os.TempDir(), limits.DiskWarningPercent); err == nil {
		out.TempDisk = &disk
	} else {
		out.DiskError = "temp disk probe unavailable"
	}
	if data, err := os.ReadFile(housekeepingResultPath()); err == nil {
		var last housekeeping.Result
		if json.Unmarshal(data, &last) == nil {
			out.LastHousekeeping = &last
		}
	}
	for _, task := range board.Tasks {
		out.Queue[task.State]++
	}
	for _, worker := range board.Workers {
		if worker.Live {
			out.Workers = append(out.Workers, worker)
		}
	}
	return out, err
}
