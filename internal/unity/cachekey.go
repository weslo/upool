package unity

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// cacheKeyTimeout bounds `unity cache key`, which hashes three files and runs no Editor
// (spike §8 D.1).
const cacheKeyTimeout = 30 * time.Second

// CacheKey runs `unity cache key <project> --format json` and returns the key it prints: a hash of
// the Editor version, packages-lock.json and the build target, which changes when a checkout
// forces a large reimport (docs/design.md, "Slot lifecycle: seed only from a Library current at
// HEAD, refresh on handout, reset without `-x`"; testdata/captures/unity/cache-key.json; A20).
// The project is positional, never --project-path, which `cache key` rejects (docs/contracts.md
// (c); testdata/captures/unity/cache-key-project-path-invalid.json). unity is found on env's
// PATH and run with env as its whole environment; it reports false, and no error, when env's PATH
// has no unity.
//
// It is this one verb's minimal entry point, which M3's verb table absorbs (#82).
func CacheKey(ctx context.Context, env []string, project string) (string, bool, error) {
	path, found := lookPath("unity", env)
	if !found {
		return "", false, nil
	}
	ctx, cancel := context.WithTimeout(ctx, cacheKeyTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, path, "cache", "key", project, "--format", "json")
	cmd.Env = env
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	runErr := cmd.Run()
	var r Response
	decodeErr := json.Unmarshal(stdout.Bytes(), &r)
	switch {
	case decodeErr == nil && !r.Success && len(r.Errors) > 0:
		return "", true, fmt.Errorf("unity cache key %s: %s: %s", project, r.Errors[0].Code,
			r.Errors[0].Message)
	case runErr != nil:
		if msg := strings.TrimSpace(stderr.String()); msg != "" {
			runErr = fmt.Errorf("%w: %s", runErr, msg)
		}
		return "", true, fmt.Errorf("unity cache key %s: %w", project, runErr)
	case decodeErr != nil:
		return "", true, fmt.Errorf("decoding unity cache key %s: %w", project, decodeErr)
	}
	var data struct {
		Key string `json:"key"`
	}
	if err := json.Unmarshal(r.Data, &data); err != nil || data.Key == "" {
		return "", true, fmt.Errorf("unity cache key %s printed no key", project)
	}
	return data.Key, true, nil
}

// lookPath finds command in env's PATH, as a shell does: the first regular, executable file of
// that name in its directories.
func lookPath(command string, env []string) (string, bool) {
	var pathValue string
	for _, kv := range env {
		if v, ok := strings.CutPrefix(kv, "PATH="); ok {
			pathValue = v
		}
	}
	for _, dir := range filepath.SplitList(pathValue) {
		if dir == "" {
			continue
		}
		file := filepath.Join(dir, command)
		if info, err := os.Stat(file); err == nil && info.Mode().IsRegular() &&
			info.Mode().Perm()&0o111 != 0 {
			return file, true
		}
	}
	return "", false
}
