package device

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestAgentIsNotStale guards the mistake that is invisible from the desktop
// side: the agent binary is built by hand, so a change to the tag reader that
// is not followed by a rebuild leaves the phone reading tags with the old
// code — and files the desktop handles fine keep failing on the phone.
//
// The check is deliberately loose. A fresh checkout gives every file the same
// timestamp, so only a binary left clearly behind its sources is reported.
func TestAgentIsNotStale(t *testing.T) {
	const slack = 2 * time.Minute

	binary, err := os.Stat(filepath.Join("agent", "mlm-agent-arm64"))
	if err != nil {
		t.Skip("no agent binary in the tree")
	}

	roots := []string{
		filepath.Join("..", "tags"),
		filepath.Join("..", "wire"),
		filepath.Join("..", "..", "cmd", "agent"),
	}

	for _, root := range roots {
		entries, err := os.ReadDir(root)
		if err != nil {
			t.Fatalf("reading %s: %v", root, err)
		}
		for _, entry := range entries {
			name := entry.Name()
			if entry.IsDir() || filepath.Ext(name) != ".go" {
				continue
			}
			if strings.HasSuffix(name, "_test.go") {
				continue // Tests are not part of the binary.
			}
			info, err := entry.Info()
			if err != nil {
				t.Fatalf("reading %s: %v", name, err)
			}
			if info.ModTime().Sub(binary.ModTime()) > slack {
				t.Errorf("%s is newer than the embedded agent — rebuild it:\n"+
					"  CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build -trimpath -buildvcs=false "+
					`-ldflags="-s -w" -o internal/device/agent/mlm-agent-arm64 ./cmd/agent`,
					filepath.Join(root, name))
			}
		}
	}
}
