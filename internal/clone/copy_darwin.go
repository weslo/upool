package clone

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"strings"
)

// cpPath is macOS's own cp, named in full: a GNU cp earlier on PATH reads -c as
// --preserve=context and would copy every byte of a Library without saying so.
const cpPath = "/bin/cp"

// Copy runs `/bin/cp -cR src dst`. A failure carries cp's stderr.
func (CP) Copy(ctx context.Context, src, dst string) error {
	cmd := exec.CommandContext(ctx, cpPath, "-cR", src, dst)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		if ctx.Err() != nil {
			err = ctx.Err()
		}
		msg := strings.TrimSpace(stderr.String())
		if msg != "" {
			return fmt.Errorf("%s -cR %s %s: %w: %s", cpPath, src, dst, err, msg)
		}
		return fmt.Errorf("%s -cR %s %s: %w", cpPath, src, dst, err)
	}
	return nil
}
