package unity

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// cacheKeyTimeout bounds `unity cache key`, which hashes three files and runs no Editor
// (spike §8 D.1).
const cacheKeyTimeout = 30 * time.Second

// CacheKey runs `unity cache key <project> --format json` through the verb table and returns the
// key it prints: a hash of the Editor version, packages-lock.json and the build target, which
// changes when a checkout forces a large reimport (docs/design.md, "Slot lifecycle: seed only from
// a Library current at HEAD, refresh on handout, reset without `-x`";
// testdata/captures/unity/cache-key.json; A20). VerbCacheKey puts the project positionally, never
// --project-path, which `cache key` rejects (docs/contracts.md (c);
// testdata/captures/unity/cache-key-project-path-invalid.json). unity is found on env's PATH and
// run with env, plus UNITY_PROJECT_PATH, as its whole environment; it reports false, and no error,
// when env's PATH has no unity. A failed call is Reply.Err's error.
func CacheKey(ctx context.Context, env []string, project string) (string, bool, error) {
	ctx, cancel := context.WithTimeout(ctx, cacheKeyTimeout)
	defer cancel()
	reply, err := Client{Runner: Exec{}, Env: env}.Call(ctx, VerbCacheKey, project)
	if err == nil {
		err = reply.Err()
	}
	switch {
	case errors.Is(err, ErrNoUnity):
		return "", false, nil
	case err != nil:
		return "", true, err
	}
	var data struct {
		Key string `json:"key"`
	}
	if err := json.Unmarshal(reply.Response.Data, &data); err != nil || data.Key == "" {
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
