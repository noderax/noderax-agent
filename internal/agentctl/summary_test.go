package agentctl

import (
	"strings"
	"testing"

	"github.com/noderax/noderax-agent/internal/config"
)

func TestRenderInstallSummaryIncludesUsefulCommands(t *testing.T) {
	t.Parallel()

	spec := platformSpec{
		Manager:     serviceManagerSystemd,
		BinaryPath:  linuxBinaryPath,
		ServiceName: linuxServiceName,
	}

	cfg := config.Default()
	cfg.APIURL = "https://api.example.com"
	cfg.ConfigFile = linuxConfigPath
	cfg.StateFile = linuxStatePath

	summary := renderInstallSummary(spec, cfg, "dev", "/workspace/config.json")

	expectedSnippets := []string{
		"Noderax Agent Ready",
		"Status       : running in the background",
		"API URL      : https://api.example.com",
		"Config       : " + linuxConfigPath,
		"Local config : /workspace/config.json",
		"start   sudo noderax-agent start",
		"status  sudo noderax-agent status",
		"config  sudo noderax-agent config show",
		"update  sudo noderax-agent config set api_url https://api.example.com",
		"Logs         : sudo journalctl -u " + linuxServiceName + " -f",
	}

	for _, snippet := range expectedSnippets {
		if !strings.Contains(summary, snippet) {
			t.Fatalf("summary missing snippet %q\n%s", snippet, summary)
		}
	}
}
