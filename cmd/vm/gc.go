package vm

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	gofrsflock "github.com/gofrs/flock"
	"github.com/projecteru2/core/log"
	"github.com/spf13/cobra"

	"github.com/cocoonstack/cocoon-macos/home"
	"github.com/cocoonstack/cocoon/cmd/cliutil"
	"github.com/cocoonstack/cocoon/gc"
	"github.com/cocoonstack/cocoon/lock/flock"
	"github.com/cocoonstack/cocoon/network"
	"github.com/cocoonstack/cocoon/utils"
)

const (
	pullTempGlob      = "pull-*.qcow2"
	gcLockName        = ".gc.lock"
	provisionedMarker = ".provisioned"
)

type vmGCSnapshot struct {
	ids    map[string]struct{}
	strays []string
}

func (s vmGCSnapshot) ActiveVMIDs() map[string]struct{} { return s.ids }

func GCCommand() *cobra.Command {
	gcCmd := &cobra.Command{
		Use:   "gc",
		Short: "Reclaim what no VM record owns: cm-family netns and TAPs, stray VM dirs, stale pull temp files",
		Args:  cobra.NoArgs,
		RunE:  GC,
	}
	gcCmd.Flags().String("cni-conf-dir", "", "CNI config dir (default /etc/cni/net.d)")
	gcCmd.Flags().String("cni-bin-dir", "", "CNI plugin dir (default /opt/cni/bin)")
	return gcCmd
}

func GC(cmd *cobra.Command, _ []string) error {
	return sweep(cliutil.CommandContext(cmd), cmd, registerNetGC)
}

// sweep holds the provisioning lock exclusively and every VM lock, so an in-flight create, clone or rm is never read as residue.
func sweep(ctx context.Context, cmd *cobra.Command, registerNet func(*gc.Orchestrator, *cobra.Command, network.VMInUse) error) error {
	if _, err := os.Stat(home.Dir(cmd)); err != nil {
		return fmt.Errorf("no state root at %s: check --state-dir or $COCOON_MACOS_HOME: %w", home.Dir(cmd), err)
	}
	gl, err := openGCLock(cmd)
	if err != nil {
		return err
	}
	locked, err := gl.TryLock()
	if err != nil {
		return fmt.Errorf("lock gc: %w", err)
	}
	if !locked {
		return errors.New("a create or clone is provisioning; retry when it finishes")
	}
	defer func() { _ = gl.Unlock() }()
	dirs, err := vmDirs(cmd)
	if err != nil {
		return err
	}
	unlock, err := tryLockVMDirs(ctx, dirs)
	if err != nil {
		return err
	}
	defer unlock()
	o := gc.New()
	gc.Register(o, vmGCModule(home.Dir(cmd), dirs))
	if len(dirs) == 0 && !utils.FileExists(filepath.Join(home.VMsDir(cmd), ".locks", provisionedMarker)) {
		log.WithFunc("cmd.vm.sweep").Warnf(ctx, "no VM dir and no provisioning marker under %s: leaving the host's netns and TAPs alone (check --state-dir or $COCOON_MACOS_HOME; a root from before this build regains them at its next create)", home.Dir(cmd))
	} else if err := registerNet(o, cmd, vmInUse(dirs)); err != nil {
		return err
	}
	return o.Run(ctx)
}

// withProvisionLock shares the gc lock for the span in which a create or clone owns host devices no record names yet.
func withProvisionLock(ctx context.Context, cmd *cobra.Command, fn func() error) error {
	gl, err := openGCLock(cmd)
	if err != nil {
		return err
	}
	if _, err := gl.TryRLockContext(ctx, 50*time.Millisecond); err != nil {
		return fmt.Errorf("wait for gc to finish: %w", err)
	}
	defer func() { _ = gl.Unlock() }()
	return fn()
}

// markProvisioned records that the state root owning vmsDir has held a VM; the marker outlives the last VM dir.
func markProvisioned(vmsDir string) error {
	path := filepath.Join(vmsDir, ".locks", provisionedMarker)
	if err := utils.EnsureDirs(filepath.Dir(path)); err != nil {
		return fmt.Errorf("create vm lock dir: %w", err)
	}
	marker, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return fmt.Errorf("mark state root provisioned: %w", err)
	}
	if err := marker.Close(); err != nil {
		return fmt.Errorf("mark state root provisioned: %w", err)
	}
	return nil
}

func openGCLock(cmd *cobra.Command) (*gofrsflock.Flock, error) {
	path := filepath.Join(home.VMsDir(cmd), ".locks", gcLockName)
	if err := utils.EnsureDirs(filepath.Dir(path)); err != nil {
		return nil, fmt.Errorf("create vm lock dir: %w", err)
	}
	return gofrsflock.New(path), nil
}

func vmDirs(cmd *cobra.Command) ([]string, error) {
	vmsDir := home.VMsDir(cmd)
	names, err := utils.ScanSubdirs(vmsDir)
	if err != nil {
		return nil, err
	}
	var dirs []string
	for _, n := range names {
		if !strings.HasPrefix(n, ".") {
			dirs = append(dirs, filepath.Join(vmsDir, n))
		}
	}
	return dirs, nil
}

func tryLockVMDirs(ctx context.Context, dirs []string) (func(), error) {
	var held []*flock.Lock
	unlock := func() {
		for _, l := range held {
			_ = l.Unlock(context.WithoutCancel(ctx))
		}
	}
	for _, dir := range dirs {
		if err := utils.EnsureDirs(filepath.Dir(vmLockPath(dir))); err != nil {
			unlock()
			return nil, fmt.Errorf("create vm lock dir: %w", err)
		}
		l := flock.NewTransient(vmLockPath(dir))
		locked, err := l.TryLock(ctx)
		if err != nil {
			unlock()
			return nil, fmt.Errorf("lock vm %s: %w", filepath.Base(dir), err)
		}
		if !locked {
			unlock()
			return nil, fmt.Errorf("vm %s is busy (a create, clone, start, stop or rm holds it); retry when it finishes", filepath.Base(dir))
		}
		held = append(held, l)
	}
	return unlock, nil
}

func vmInUse(dirs []string) network.VMInUse {
	return func(_ context.Context, vmID string) (bool, error) {
		for _, dir := range dirs {
			r, err := loadRec(dir)
			if err != nil {
				if errors.Is(err, os.ErrNotExist) {
					continue
				}
				return false, fmt.Errorf("read vm %s for gc: %w", filepath.Base(dir), err)
			}
			if r.VMID == vmID {
				return isRunning(r), nil
			}
		}
		return false, nil
	}
}

func vmGCModule(stateDir string, dirs []string) gc.Module[vmGCSnapshot] {
	return gc.Module[vmGCSnapshot]{
		Name: "vm",
		ReadDB: func(context.Context) (vmGCSnapshot, error) {
			snap := vmGCSnapshot{ids: make(map[string]struct{})}
			for _, dir := range dirs {
				r, err := loadRec(dir)
				if err != nil {
					if !errors.Is(err, os.ErrNotExist) {
						return vmGCSnapshot{}, fmt.Errorf("read vm %s for gc: %w", filepath.Base(dir), err)
					}
					snap.strays = append(snap.strays, dir)
					continue
				}
				snap.ids[r.VMID] = struct{}{}
			}
			return snap, nil
		},
		Resolve: func(_ context.Context, snap vmGCSnapshot, _ map[string]any) []string { return snap.strays },
		Collect: func(ctx context.Context, strays []string, _ vmGCSnapshot) error {
			logger := log.WithFunc("gc.vm")
			var errs []error
			for _, dir := range strays {
				if err := resetIncompleteVMDir(ctx, dir); err != nil {
					errs = append(errs, err)
					continue
				}
				logger.Infof(ctx, "collected id=%s reason=stray-dir", filepath.Base(dir))
			}
			cutoff := time.Now().Add(-utils.StaleTempAge)
			errs = append(errs, utils.RemoveMatching(ctx, stateDir, func(e os.DirEntry) bool {
				if ok, _ := filepath.Match(pullTempGlob, e.Name()); !ok || e.IsDir() {
					return false
				}
				info, err := e.Info()
				return err == nil && info.ModTime().Before(cutoff)
			})...)
			return errors.Join(errs...)
		},
	}
}
