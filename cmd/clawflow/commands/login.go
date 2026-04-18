package commands

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os/exec"
	"runtime"
	"time"

	"github.com/spf13/cobra"
	"github.com/zhoushoujianwork/clawflow/internal/config"
)

func NewLoginCmd() *cobra.Command {
	var saasURL string

	cmd := &cobra.Command{
		Use:   "login",
		Short: "OAuth login to ClawFlow SaaS and obtain a worker token",
		Long: `Opens a browser for GitHub/GitLab OAuth, receives the callback on localhost,
exchanges the code for a JWT, then generates a worker token (cfw_xxx) and writes
saas_url + worker_token to ~/.clawflow/config/worker.yaml.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runLogin(saasURL)
		},
	}

	cmd.Flags().StringVar(&saasURL, "saas-url", "", "ClawFlow SaaS base URL (required)")
	_ = cmd.MarkFlagRequired("saas-url")
	return cmd
}

func runLogin(saasURL string) error {
	// 1. Start a local HTTP server to receive the OAuth callback.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return fmt.Errorf("cannot start callback server: %w", err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	redirectURI := fmt.Sprintf("http://localhost:%d/callback", port)

	codeCh := make(chan string, 1)
	errCh := make(chan error, 1)

	srv := &http.Server{}
	mux := http.NewServeMux()
	mux.HandleFunc("/callback", func(w http.ResponseWriter, r *http.Request) {
		code := r.URL.Query().Get("code")
		if code == "" {
			errCh <- fmt.Errorf("no code in callback: %s", r.URL.RawQuery)
			http.Error(w, "missing code", http.StatusBadRequest)
			return
		}
		fmt.Fprintln(w, "<html><body><h2>Login successful — you can close this tab.</h2></body></html>")
		codeCh <- code
	})
	srv.Handler = mux

	go func() {
		if err := srv.Serve(ln); err != nil && err != http.ErrServerClosed {
			errCh <- err
		}
	}()

	// 2. Build the OAuth URL and open the browser.
	authURL := fmt.Sprintf("%s/api/auth/github?redirect_uri=%s", saasURL, redirectURI)
	fmt.Printf("Opening browser for OAuth login...\n  %s\n\n", authURL)
	_ = openBrowser(authURL)

	// 3. Wait for the callback (2-minute timeout).
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	var code string
	select {
	case code = <-codeCh:
	case err := <-errCh:
		return fmt.Errorf("OAuth callback error: %w", err)
	case <-ctx.Done():
		return fmt.Errorf("timed out waiting for OAuth callback")
	}
	_ = srv.Shutdown(context.Background())

	// 4. Exchange code for JWT.
	jwt, err := exchangeCodeForJWT(saasURL, code, redirectURI)
	if err != nil {
		return fmt.Errorf("JWT exchange failed: %w", err)
	}

	// 5. Generate worker token.
	workerToken, err := generateWorkerToken(saasURL, jwt)
	if err != nil {
		return fmt.Errorf("worker token generation failed: %w", err)
	}

	// 6. Persist config.
	wc := &config.WorkerConfig{
		SaasURL:     saasURL,
		WorkerToken: workerToken,
	}
	if err := wc.Save(); err != nil {
		return fmt.Errorf("save worker config: %w", err)
	}

	fmt.Printf("Login successful.\n")
	fmt.Printf("  saas_url:     %s\n", saasURL)
	fmt.Printf("  worker_token: %s***\n", workerToken[:min(8, len(workerToken))])
	fmt.Printf("  config:       ~/.clawflow/config/worker.yaml\n")
	fmt.Println("\nRun: clawflow worker start")
	return nil
}

func exchangeCodeForJWT(saasURL, code, redirectURI string) (string, error) {
	body, _ := json.Marshal(map[string]string{
		"code":         code,
		"redirect_uri": redirectURI,
	})
	resp, err := http.Post(saasURL+"/api/auth/github", "application/json", bytes.NewReader(body)) //nolint:gosec
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(resp.Body)
		return "", fmt.Errorf("status %d: %s", resp.StatusCode, b)
	}
	var result struct {
		Token string `json:"token"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return "", err
	}
	if result.Token == "" {
		return "", fmt.Errorf("empty token in response")
	}
	return result.Token, nil
}

func generateWorkerToken(saasURL, jwt string) (string, error) {
	req, _ := http.NewRequest("POST", saasURL+"/api/v1/orgs/current/worker-token", nil)
	req.Header.Set("Authorization", "Bearer "+jwt)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(resp.Body)
		return "", fmt.Errorf("status %d: %s", resp.StatusCode, b)
	}
	var result struct {
		Token string `json:"token"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return "", err
	}
	if result.Token == "" {
		return "", fmt.Errorf("empty worker token in response")
	}
	return result.Token, nil
}

func openBrowser(url string) error {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		cmd = exec.Command("open", url)
	case "windows":
		cmd = exec.Command("rundll32", "url.dll,FileProtocolHandler", url)
	default:
		cmd = exec.Command("xdg-open", url)
	}
	return cmd.Start()
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
