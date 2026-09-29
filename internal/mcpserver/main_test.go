package mcpserver

import (
	"os"
	"testing"

	"github.com/wendellrocha/agentclip/internal/testenv"
)

// TestMain keeps these tests away from the developer's real inbox and cache.
func TestMain(m *testing.M) { os.Exit(testenv.Main(m)) }
