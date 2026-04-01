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
		"config-show sudo noderax-agent config show",
		"config-set  sudo noderax-agent config set api_url https://api.example.com",
		"update      sudo noderax-agent update --target-version 1.2.3 --target-id <rollout-target-id>",
		"remove  sudo noderax-agent uninstall",
		"Logs         : sudo journalctl -u " + linuxServiceName + " -f",
	}

	for _, snippet := range expectedSnippets {
		if !strings.Contains(summary, snippet) {
			t.Fatalf("summary missing snippet %q\n%s", snippet, summary)
		}
	}
}

func TestRenderPrivilegedUpdateHelperTargetsManagedBinary(t *testing.T) {
	t.Parallel()

	spec := platformSpec{
		BinaryPath:                 linuxBinaryPath,
		PrivilegedUpdateHelperPath: linuxPrivilegedUpdateHelperPath,
	}

	script := renderPrivilegedUpdateHelper(spec)

	expectedSnippets := []string{
		"usage: " + linuxPrivilegedUpdateHelperPath,
		"exec \"" + linuxBinaryPath + "\" update --target-version \"$2\" --target-id \"$4\" --rollback",
		"exec \"" + linuxBinaryPath + "\" update --target-version \"$2\" --target-id \"$4\"",
	}

	for _, snippet := range expectedSnippets {
		if !strings.Contains(script, snippet) {
			t.Fatalf("helper script missing snippet %q\n%s", snippet, script)
		}
	}
}
