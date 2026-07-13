package perception

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"time"
)

// StartHailoRTAdapter supervises the provider-specific process while the
// neutral Unix socket remains owned by Atlas Agent. No shell is involved, so
// the executable and socket path cannot be interpreted as commands.
func StartHailoRTAdapter(ctx context.Context, logger *slog.Logger, executable, socketPath string) error {
	resolved, err := exec.LookPath(executable)
	if err != nil {
		return fmt.Errorf("find HailoRT adapter %q: %w", executable, err)
	}
	if logger == nil {
		logger = slog.Default()
	}
	go superviseHailoRTAdapter(ctx, logger, resolved, socketPath)
	return nil
}

func superviseHailoRTAdapter(ctx context.Context, logger *slog.Logger, executable, socketPath string) {
	retry := 2 * time.Second
	for ctx.Err() == nil {
		command := exec.CommandContext(ctx, executable, "--socket", socketPath)
		command.Stdout = os.Stdout
		command.Stderr = os.Stderr
		logger.Info("starting HailoRT perception adapter", "executable", executable)
		err := command.Run()
		if ctx.Err() != nil {
			return
		}
		logger.Error("HailoRT perception adapter exited", "error", err, "retry_after", retry)
		timer := time.NewTimer(retry)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}
		if retry < 30*time.Second {
			retry *= 2
			if retry > 30*time.Second {
				retry = 30 * time.Second
			}
		}
	}
}
