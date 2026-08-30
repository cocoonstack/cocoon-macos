package procutil

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/cocoonstack/cocoon/utils"
)

// TerminateByCmdline stops matching processes after verifying their command-line identity.
func TerminateByCmdline(ctx context.Context, binary, path string, grace time.Duration) error {
	pids, err := utils.FindVMMByCmdline(binary, path)
	if err != nil {
		return fmt.Errorf("scan %s processes for %s: %w", binary, path, err)
	}
	var errs []error
	for _, pid := range pids {
		if err := utils.TerminateProcess(ctx, pid, binary, path, grace); err != nil {
			errs = append(errs, fmt.Errorf("terminate %s pid %d: %w", binary, pid, err))
		}
	}
	return errors.Join(errs...)
}
