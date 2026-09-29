package bridge

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// isolateCache points every per-user directory at a temporary one, because
// os.UserCacheDir reads XDG_CACHE_HOME on Linux, HOME on macOS and LocalAppData
// on Windows, and these tests write into the inbox.
func isolateCache(t *testing.T) {
	t.Helper()
	home := t.TempDir()
	for _, name := range []string{"HOME", "USERPROFILE", "XDG_CACHE_HOME", "XDG_CONFIG_HOME", "LocalAppData", "AppData"} {
		t.Setenv(name, home)
	}
}

func TestValidInboundNameAcceptsOnlyOnePlainFileName(t *testing.T) {
	valid := []string{"report.csv", "relatório final.csv", "my file (1).txt", ".env", "a", strings.Repeat("a", 255), "arquivo.tar.gz", "日本語.txt"}
	for _, name := range valid {
		if !validInboundName(name) {
			t.Errorf("validInboundName(%q) = false, want true", name)
		}
	}
	invalid := map[string]string{
		"empty":                   "",
		"current directory":       ".",
		"parent directory":        "..",
		"slash":                   "a/b",
		"leading slash":           "/etc/passwd",
		"traversal":               "../../etc/passwd",
		"backslash":               `a\b`,
		"windows traversal":       `..\..\x`,
		"trailing dot":            "report.",
		"trailing space":          "report.csv ",
		"NUL":                     "a\x00b",
		"newline":                 "a\nb",
		"control character":       "a\x01b",
		"DEL":                     "a\x7fb",
		"longer than a file name": strings.Repeat("a", 256),
	}
	for description, name := range invalid {
		if validInboundName(name) {
			t.Errorf("validInboundName(%s = %q) = true, want false", description, name)
		}
	}
}

func TestValidInboundNameRefusesWindowsSpecialCharactersOnWindows(t *testing.T) {
	for _, name := range []string{"file.txt:stream", "a<b", "a>b", `a"b`, "a|b", "a?b", "a*b"} {
		got := validInboundName(name)
		if want := runtime.GOOS != "windows"; got != want {
			t.Errorf("validInboundName(%q) = %v on %s, want %v", name, got, runtime.GOOS, want)
		}
	}
}

func TestInboundProfileNeverLeavesTheInbox(t *testing.T) {
	for sessionID, want := range map[string]string{
		"companion:m2":                         "m2",
		"companion:bastion-m2_2":               "bastion-m2_2",
		"companion:..":                         "default",
		"companion:.":                          "default",
		"companion:":                           "default",
		"companion:a/b":                        "default",
		`companion:a\b`:                        "default",
		"companion:../../x":                    "default",
		"companion:with space":                 "default",
		"companion:" + strings.Repeat("a", 65): "default",
		"anything-else":                        "anything-else",
		"../escape":                            "default",
	} {
		if got := inboundProfile(sessionID); got != want {
			t.Errorf("inboundProfile(%q) = %q, want %q", sessionID, got, want)
		}
	}
}

func TestWithinRejectsEveryWayOutOfTheRoot(t *testing.T) {
	root := filepath.Join(t.TempDir(), "received")
	for target, want := range map[string]bool{
		filepath.Join(root, "m2", "id"):        true,
		filepath.Join(root, "a"):               true,
		root:                                   false,
		filepath.Join(root, "..", "sibling"):   false,
		filepath.Dir(root):                     false,
		filepath.Join(root, "..", "received2"): false,
	} {
		if got := within(root, target); got != want {
			t.Errorf("within(%q, %q) = %v, want %v", root, target, got, want)
		}
	}
}

func TestCreateInboundOfferRefusesUnsafeNames(t *testing.T) {
	b := New(time.Minute)
	if err := b.RegisterPersistentSessionWithUpload("companion:dev", "read-token", "upload-token"); err != nil {
		t.Fatal(err)
	}
	checksum := strings.Repeat("a", 64)
	for _, name := range []string{"..", ".", "", "a/b", "trailing.", "a\x00b"} {
		if _, err := b.CreateInboundOffer("companion:dev", name, 1, checksum); err == nil {
			t.Errorf("an offer named %q was accepted", name)
		}
	}
}

func TestDeliveryUsesTheDefaultFolderWhenTheSessionNamesAnUnsafeProfile(t *testing.T) {
	isolateCache(t)
	cache, err := os.UserCacheDir()
	if err != nil {
		t.Skip("no user cache directory")
	}
	b := New(time.Minute)
	// A session id that is not a valid profile name must not choose where files land.
	if err := b.RegisterPersistentSessionWithUpload("companion:..", "read-token", "upload-token"); err != nil {
		t.Fatal(err)
	}
	empty := sha256.Sum256(nil)
	offer, err := b.CreateInboundOffer("companion:..", "report.csv", 0, hex.EncodeToString(empty[:]))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := b.AcceptInboundOffer(offer.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := b.DeliverInboundOffer("companion:..", offer.ID, strings.NewReader(""), 0); err != nil {
		t.Fatal(err)
	}
	status := b.InboundLocalStatus()
	if len(status.Received) != 1 {
		t.Fatalf("received = %+v", status.Received)
	}
	path := status.Received[0].Path
	root := filepath.Join(cache, "agentclip", "received")
	if !within(root, path) || !strings.HasPrefix(path, filepath.Join(root, "default")+string(filepath.Separator)) {
		t.Fatalf("file landed in %q, want it under %q", path, filepath.Join(root, "default"))
	}
}

func TestWriteInboundFileRefusesAnUnsafeDestinationBeforeTouchingTheDisk(t *testing.T) {
	isolateCache(t)
	cache, err := os.UserCacheDir()
	if err != nil {
		t.Skip("no user cache directory")
	}
	empty := hex.EncodeToString(func() []byte { s := sha256.Sum256(nil); return s[:] }())
	tests := map[string][3]string{
		"profile with traversal": {"..", "0123456789abcdef", "a.txt"},
		"offer id with slash":    {"m2", "../../x", "a.txt"},
		"name with traversal":    {"m2", "0123456789abcdef", "../a.txt"},
		"empty offer id":         {"m2", "", "a.txt"},
	}
	for description, arguments := range tests {
		t.Run(description, func(t *testing.T) {
			path, err := writeInboundFile(arguments[0], arguments[1], arguments[2], 0, empty, strings.NewReader(""))
			if err == nil {
				t.Fatalf("wrote %q", path)
			}
		})
	}
	if _, err := os.Stat(filepath.Join(cache, "agentclip", "x")); err == nil {
		t.Fatal("a refused write still created a directory outside the inbox")
	}
}

func TestValidateFileRefRequiresAnAbsoluteNormalizedPath(t *testing.T) {
	file := filepath.Join(t.TempDir(), "report.csv")
	if err := os.WriteFile(file, []byte("a,b\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	item, err := FileItem(file, "text/csv")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := validateFileRef(*item.File); err != nil {
		t.Fatalf("a real absolute path was refused: %v", err)
	}
	relative, err := filepath.Rel(mustWD(t), file)
	if err != nil {
		t.Skip("no relative path to the temp file on this platform")
	}
	// filepath.Join cleans, so build the un-normalized path by hand.
	uncleanPath := filepath.Dir(file) + string(filepath.Separator) + "sub" + string(filepath.Separator) + ".." + string(filepath.Separator) + filepath.Base(file)
	for description, path := range map[string]string{"relative": relative, "with .. segments": uncleanPath} {
		ref := *item.File
		ref.Path = path
		if _, err := validateFileRef(ref); err == nil {
			t.Errorf("%s path %q was accepted", description, path)
		}
	}
}

func mustWD(t *testing.T) string {
	t.Helper()
	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	return wd
}
