package vm

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/spf13/cobra"

	"github.com/cocoonstack/cocoon-macos/home"
	"github.com/cocoonstack/cocoon/gc"
	"github.com/cocoonstack/cocoon/lock/flock"
	"github.com/cocoonstack/cocoon/network"
)

func TestGCReclaimsStrayDirsAndStalePulls(t *testing.T) {
	cmd := newLifecycleTestCommand(t, t.TempDir())
	vmsDir := home.VMsDir(cmd)
	kept := filepath.Join(vmsDir, "kept")
	stray := filepath.Join(vmsDir, "stray")
	for _, d := range []string{kept, stray, filepath.Join(vmsDir, ".locks")} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := saveRec(kept, &record{Name: "kept", VMID: "KEPTVMID"}); err != nil {
		t.Fatal(err)
	}
	stalePull := filepath.Join(home.Dir(cmd), "pull-stale.qcow2")
	freshPull := filepath.Join(home.Dir(cmd), "pull-fresh.qcow2")
	for _, p := range []string{stalePull, freshPull} {
		if err := os.WriteFile(p, []byte("partial"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	old := time.Now().Add(-2 * time.Hour)
	if err := os.Chtimes(stalePull, old, old); err != nil {
		t.Fatal(err)
	}

	if err := sweep(t.Context(), cmd, noNetGC); err != nil {
		t.Fatalf("sweep: %v", err)
	}
	for p, want := range map[string]bool{kept: true, stray: false, stalePull: false, freshPull: true} {
		_, err := os.Stat(p)
		if (err == nil) != want {
			t.Errorf("%s exists=%v, want %v", p, err == nil, want)
		}
	}
	if _, err := os.Stat(filepath.Join(vmsDir, ".locks")); err != nil {
		t.Errorf(".locks must survive: %v", err)
	}
}

func TestGCRunsHostCollectorsOnlyForARootThatHeldAVM(t *testing.T) {
	cmd := newLifecycleTestCommand(t, filepath.Join(t.TempDir(), "missing"))
	if err := sweep(t.Context(), cmd, noNetGC); err == nil || !strings.Contains(err.Error(), "--state-dir") {
		t.Fatalf("sweep of a state root that does not exist = %v, want a refusal naming the flag", err)
	}

	cmd = newLifecycleTestCommand(t, t.TempDir())
	stalePull := filepath.Join(home.Dir(cmd), "pull-stale.qcow2")
	if err := os.WriteFile(stalePull, []byte("partial"), 0o600); err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-2 * time.Hour)
	if err := os.Chtimes(stalePull, old, old); err != nil {
		t.Fatal(err)
	}
	if err := withProvisionLock(t.Context(), cmd, func() error { return errors.New("image not in this store") }); err == nil {
		t.Fatal("the failing create must surface its error")
	}
	net := &netGCSpy{}
	if err := sweep(t.Context(), cmd, net.register); err != nil {
		t.Fatalf("sweep of a root that never held a VM: %v", err)
	}
	if net.calls != 0 {
		t.Fatalf("host collectors ran %d times on a root that never held a VM", net.calls)
	}
	if _, err := os.Stat(stalePull); !os.IsNotExist(err) {
		t.Errorf("the root's own stale pull temp survived: %v", err)
	}
	if err := markProvisioned(home.VMsDir(cmd)); err != nil {
		t.Fatal(err)
	}
	if err := sweep(t.Context(), cmd, net.register); err != nil || net.calls != 1 {
		t.Fatalf("sweep after provisioning: err=%v collectors ran %d times, want 1", err, net.calls)
	}
}

func TestApplyNetMarksTheRootProvisioned(t *testing.T) {
	cmd := newLifecycleTestCommand(t, t.TempDir())
	if err := applyNet(cmd, &record{Name: "u", NetMode: netUser}); err != nil {
		t.Fatalf("applyNet: %v", err)
	}
	if _, err := os.Stat(filepath.Join(home.VMsDir(cmd), ".locks", provisionedMarker)); err != nil {
		t.Fatalf("marker after applyNet: %v", err)
	}
}

func TestRemovingAStrayDirKeepsTheRootEligible(t *testing.T) {
	for name, remove := range map[string]func(*testing.T, *cobra.Command, string){
		"gc collects it": func(t *testing.T, cmd *cobra.Command, _ string) {
			if err := sweep(t.Context(), cmd, noNetGC); err != nil {
				t.Fatalf("sweep: %v", err)
			}
		},
		"vm rm removes it": func(t *testing.T, cmd *cobra.Command, stray string) {
			cmd.Flags().Bool("force", false, "")
			if err := RM(cmd, []string{filepath.Base(stray)}); err != nil {
				t.Fatalf("rm: %v", err)
			}
		},
	} {
		t.Run(name, func(t *testing.T) {
			cmd := newLifecycleTestCommand(t, t.TempDir())
			stray := filepath.Join(home.VMsDir(cmd), "stray")
			if err := os.MkdirAll(stray, 0o755); err != nil {
				t.Fatal(err)
			}
			remove(t, cmd, stray)
			if _, err := os.Stat(stray); !os.IsNotExist(err) {
				t.Fatalf("stray dir survived: %v", err)
			}
			net := &netGCSpy{}
			if err := sweep(t.Context(), cmd, net.register); err != nil || net.calls != 1 {
				t.Fatalf("sweep after the only VM dir went away: err=%v collectors ran %d times, want 1 (a leaked netns needs the retry)", err, net.calls)
			}
		})
	}
}

func TestGCLockCannotBeAVMLock(t *testing.T) {
	if name := strings.TrimSuffix(gcLockName, ".lock"); validName.MatchString(name) {
		t.Fatalf("%q is a legal VM name, so vm create --name %s would hold the provisioning lock as its own and deadlock", name, name)
	}
}

func TestGCRefusesWhileACreateProvisions(t *testing.T) {
	cmd := newLifecycleTestCommand(t, t.TempDir())
	if err := os.MkdirAll(home.VMsDir(cmd), 0o755); err != nil {
		t.Fatal(err)
	}
	err := withProvisionLock(t.Context(), cmd, func() error { return sweep(t.Context(), cmd, noNetGC) })
	if err == nil || !strings.Contains(err.Error(), "provisioning") {
		t.Fatalf("sweep under a provisioning lock = %v, want a refusal", err)
	}
	if err := sweep(t.Context(), cmd, noNetGC); err != nil {
		t.Fatalf("sweep after the provisioning lock was released: %v", err)
	}
}

func TestGCRefusesABusyVM(t *testing.T) {
	cmd := newLifecycleTestCommand(t, t.TempDir())
	dir := filepath.Join(home.VMsDir(cmd), "busy")
	for _, d := range []string{dir, filepath.Dir(vmLockPath(dir))} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := saveRec(dir, &record{Name: "busy", VMID: "BUSYVMID"}); err != nil {
		t.Fatal(err)
	}
	held := flock.NewTransient(vmLockPath(dir))
	if err := held.Lock(t.Context()); err != nil {
		t.Fatal(err)
	}
	defer held.Unlock(t.Context()) //nolint:errcheck

	err := sweep(t.Context(), cmd, noNetGC)
	if err == nil || !strings.Contains(err.Error(), "busy") {
		t.Fatalf("sweep = %v, want a busy refusal", err)
	}
	if _, err := os.Stat(dir); err != nil {
		t.Errorf("busy dir touched: %v", err)
	}
}

func TestGCRefusesUnreadableRecord(t *testing.T) {
	cmd := newLifecycleTestCommand(t, t.TempDir())
	dir := filepath.Join(home.VMsDir(cmd), "owned")
	if err := os.MkdirAll(filepath.Join(dir, "vm.json"), 0o755); err != nil {
		t.Fatal(err)
	}
	collected := false
	err := sweep(t.Context(), cmd, func(o *gc.Orchestrator, _ *cobra.Command, _ network.VMInUse) error {
		gc.Register(o, gc.Module[struct{}]{
			Name:   "network",
			ReadDB: func(context.Context) (struct{}, error) { return struct{}{}, nil },
			Resolve: func(context.Context, struct{}, map[string]any) []string {
				return []string{"owned"}
			},
			Collect: func(context.Context, []string, struct{}) error {
				collected = true
				return nil
			},
		})
		return nil
	})
	if err == nil || !strings.Contains(err.Error(), "read vm owned for gc") {
		t.Fatalf("sweep = %v, want the record read error", err)
	}
	if collected {
		t.Fatal("network was collected without a complete ownership snapshot")
	}
}

func noNetGC(*gc.Orchestrator, *cobra.Command, network.VMInUse) error { return nil }

type netGCSpy struct{ calls int }

func (s *netGCSpy) register(*gc.Orchestrator, *cobra.Command, network.VMInUse) error {
	s.calls++
	return nil
}
