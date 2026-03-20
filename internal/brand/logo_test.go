package brand

import (
	"bytes"
	"strings"
	"testing"
)

func TestPrintLogoWritesBanner(t *testing.T) {
	t.Parallel()

	var buffer bytes.Buffer
	PrintLogo(&buffer)

	output := buffer.String()
	if strings.TrimSpace(output) == "" {
		t.Fatal("PrintLogo() wrote an empty banner")
	}
	if !strings.Contains(output, "→→") {
		t.Fatalf("PrintLogo() output does not contain expected art marker: %q", output)
	}
}
