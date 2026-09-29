package companion

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// Logger writes private, human-readable Companion diagnostics. It deliberately
// records metadata and errors only; clipboard bytes and pairing credentials
// must never be written here.
type Logger struct {
	mu       sync.Mutex
	file     *os.File
	path     string
	debug    bool
	size     int64
	maxBytes int64
}

// maxLogBytes bounds the live log; one previous generation is kept as .1.
const maxLogBytes = 5 << 20

// ErrNoLog means the profile has never written a log on this machine.
var ErrNoLog = errors.New("no log for this profile")

// RequireLog checks that a log exists for the profile without creating one, so
// asking for the logs of a name that was never used leaves nothing behind.
func RequireLog(profile string) error {
	if !validProfileName(profile) {
		return fmt.Errorf("invalid Companion profile %q", profile)
	}
	dir, err := os.UserCacheDir()
	if err != nil {
		return fmt.Errorf("find AgentClip cache directory: %w", err)
	}
	if info, err := os.Stat(filepath.Join(dir, "agentclip", "logs", profile+".log")); err != nil || !info.Mode().IsRegular() {
		return fmt.Errorf("%w: %q", ErrNoLog, profile)
	}
	return nil
}

func NewLogger(profile string) (*Logger, error) {
	if !validProfileName(profile) {
		return nil, fmt.Errorf("invalid Companion profile %q", profile)
	}
	dir, err := os.UserCacheDir()
	if err != nil {
		return nil, fmt.Errorf("find AgentClip cache directory: %w", err)
	}
	dir = filepath.Join(dir, "agentclip", "logs")
	if err := os.MkdirAll(dir, 0700); err != nil {
		return nil, fmt.Errorf("create AgentClip log directory: %w", err)
	}
	path := filepath.Join(dir, profile+".log")
	file, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		return nil, fmt.Errorf("open Companion log: %w", err)
	}
	_ = os.Chmod(path, 0600)
	var size int64
	if info, err := file.Stat(); err == nil {
		size = info.Size()
	}
	return &Logger{file: file, path: path, size: size, maxBytes: maxLogBytes, debug: strings.EqualFold(os.Getenv("AGENTCLIP_LOG_LEVEL"), "debug")}, nil
}

func (l *Logger) Path() string { return l.path }

func (l *Logger) Close() error {
	if l == nil || l.file == nil {
		return nil
	}
	return l.file.Close()
}

func (l *Logger) Info(message string, args ...any)  { l.write("INFO", message, args...) }
func (l *Logger) Error(message string, args ...any) { l.write("ERROR", message, args...) }
func (l *Logger) Debug(message string, args ...any) {
	if l != nil && l.debug {
		l.write("DEBUG", message, args...)
	}
}

func (l *Logger) write(level, message string, args ...any) {
	if l == nil || l.file == nil {
		return
	}
	// One event is one line: escape newlines so values derived from requests
	// cannot forge additional entries.
	text := lineEscaper.Replace(fmt.Sprintf(message, args...))
	line := fmt.Sprintf("%s [%s] %s\n", time.Now().UTC().Format(time.RFC3339Nano), level, text)
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.maxBytes > 0 && l.size+int64(len(line)) > l.maxBytes {
		l.rotateLocked()
	}
	n, _ := l.file.WriteString(line)
	l.size += int64(n)
}

var lineEscaper = strings.NewReplacer("\n", `\n`, "\r", `\r`)

// rotateLocked moves the full log to <path>.1 and starts a fresh file. The
// file is closed first because Windows cannot rename an open file. If the
// rename still fails, the log is truncated instead, so it stays bounded.
func (l *Logger) rotateLocked() {
	_ = l.file.Close()
	flags := os.O_CREATE | os.O_APPEND | os.O_WRONLY
	if err := os.Rename(l.path, l.path+".1"); err != nil {
		flags |= os.O_TRUNC
	}
	file, err := os.OpenFile(l.path, flags, 0600)
	if err != nil {
		return
	}
	l.file, l.size = file, 0
}

// Export copies the current log to a user-selected file without exposing the
// live log path to any remote process.
func (l *Logger) Export(destination string) error {
	if strings.TrimSpace(destination) == "" {
		return fmt.Errorf("log export destination is required")
	}
	input, err := os.Open(l.path)
	if err != nil {
		return fmt.Errorf("read Companion log: %w", err)
	}
	defer input.Close()
	output, err := os.OpenFile(destination, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0600)
	if err != nil {
		return fmt.Errorf("create log export: %w", err)
	}
	if _, err = io.Copy(output, input); err != nil {
		_ = output.Close()
		return fmt.Errorf("export Companion log: %w", err)
	}
	if err = output.Close(); err != nil {
		return fmt.Errorf("close log export: %w", err)
	}
	_ = os.Chmod(destination, 0600)
	return nil
}
