package gitexec

import (
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/zhoushoujianwork/clawflow/internal/config"
)

// AppEnvironment lets Git invoked by an implementation agent resolve fresh
// credentials through this executable. No token or private key enters the env.
func AppEnvironment(env []string) ([]string, error) {
	cfg, err := config.Load()
	if err != nil {
		if _, e := os.Stat(config.ConfigPath()); os.IsNotExist(e) {
			return env, nil
		}
		return nil, fmt.Errorf("github_app: cannot load local authentication")
	}
	if !cfg.Settings.GitHubApp.Enabled() {
		return env, nil
	}
	executable, err := os.Executable()
	if err != nil {
		return nil, fmt.Errorf("github_app: credential executable unavailable")
	}
	helper := "!'" + strings.ReplaceAll(executable, "'", "'\\''") + "' git-credential"
	count := 0
	for _, v := range env {
		if strings.HasPrefix(v, "GIT_CONFIG_COUNT=") {
			count, _ = strconv.Atoi(strings.TrimPrefix(v, "GIT_CONFIG_COUNT="))
		}
	}
	if count < 0 || count > 1000 {
		return nil, fmt.Errorf("github_app: invalid Git environment")
	}
	result := make([]string, 0, len(env)+12)
	for _, v := range env {
		if !strings.HasPrefix(v, "GIT_CONFIG_COUNT=") {
			result = append(result, v)
		}
	}
	for _, pair := range [][2]string{{"credential.https://github.com.helper", ""}, {"credential.https://github.com.helper", helper}, {"credential.https://github.com.useHttpPath", "true"}, {"url.https://github.com/.insteadOf", "git@github.com:"}} {
		result = append(result, fmt.Sprintf("GIT_CONFIG_KEY_%d=%s", count, pair[0]), fmt.Sprintf("GIT_CONFIG_VALUE_%d=%s", count, pair[1]))
		count++
	}
	return append(result, fmt.Sprintf("GIT_CONFIG_COUNT=%d", count)), nil
}
