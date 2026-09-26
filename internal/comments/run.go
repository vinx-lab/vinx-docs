package comments

import (
	"context"
	"os/exec"
	"time"
)

func runQuiet(command []string, timeout time.Duration) bool {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, command[0], command[1:]...)
	return cmd.Run() == nil
}
