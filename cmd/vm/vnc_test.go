package vm

import (
	"errors"
	"testing"
)

func TestRequireCNIVNCPassword(t *testing.T) {
	tests := []struct {
		name    string
		isCNI   bool
		vncDisp int
		vncPass string
		wantErr bool
	}{
		{"cni vnc no password rejected", true, 0, "", true},
		{"cni vnc with password ok", true, 0, "s3cret", false},
		{"cni vnc disabled ok", true, -1, "", false},
		{"non-cni vnc no password ok", false, 0, "", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := requireCNIVNCPassword(tt.isCNI, tt.vncDisp, tt.vncPass)
			if tt.wantErr && !errors.Is(err, errCNIVNCPassRequired) {
				t.Errorf("got %v, want errCNIVNCPassRequired", err)
			}
			if !tt.wantErr && err != nil {
				t.Errorf("got %v, want nil", err)
			}
		})
	}
}
