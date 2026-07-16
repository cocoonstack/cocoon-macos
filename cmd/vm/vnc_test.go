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

func TestValidateVNCPassword(t *testing.T) {
	tests := []struct {
		name    string
		pw      string
		wantErr bool
	}{
		{"empty ok", "", false},
		{"normal ok", "s3cret", false},
		{"max length ok", "8charpwd", false},
		{"too long rejected", "9charpass", true},
		{"newline injection rejected", "x\nquit", true},
		{"carriage return rejected", "x\rset", true},
		{"tab rejected", "a\tb", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := validateVNCPassword(tt.pw); (err != nil) != tt.wantErr {
				t.Errorf("validateVNCPassword(%q) err = %v, wantErr %v", tt.pw, err, tt.wantErr)
			}
		})
	}
}
