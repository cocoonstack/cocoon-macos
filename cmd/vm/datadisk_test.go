package vm

import (
	"slices"
	"testing"
)

func TestParseDataDisks(t *testing.T) {
	const gib = 1 << 30
	tests := []struct {
		name      string
		raw       []string
		reserved  []string
		wantErr   bool
		wantNames []string
		wantSizes []int64
	}{
		{name: "single default name", raw: []string{"size=1G"}, wantNames: []string{"data1"}, wantSizes: []int64{gib}},
		{name: "explicit name", raw: []string{"name=logs,size=512M"}, wantNames: []string{"logs"}, wantSizes: []int64{512 << 20}},
		{name: "multiple default names count up", raw: []string{"size=1G", "size=2G"}, wantNames: []string{"data1", "data2"}, wantSizes: []int64{gib, 2 * gib}},
		{name: "auto name skips an explicit one", raw: []string{"name=data1,size=1G", "size=1G"}, wantNames: []string{"data1", "data2"}, wantSizes: []int64{gib, gib}},
		{name: "size at 16MiB minimum", raw: []string{"size=16M"}, wantNames: []string{"data1"}, wantSizes: []int64{16 << 20}},
		{name: "four disks is the cap", raw: []string{"size=1G", "size=1G", "size=1G", "size=1G"}, wantNames: []string{"data1", "data2", "data3", "data4"}, wantSizes: []int64{gib, gib, gib, gib}},

		{name: "empty spec", raw: []string{""}, wantErr: true},
		{name: "missing size", raw: []string{"name=foo"}, wantErr: true},
		{name: "size below minimum", raw: []string{"size=1M"}, wantErr: true},
		{name: "size not parseable", raw: []string{"size=notabyte"}, wantErr: true},
		{name: "not key=value", raw: []string{"size"}, wantErr: true},
		{name: "bad name uppercase", raw: []string{"name=Bad,size=1G"}, wantErr: true},
		{name: "bad name cocoon prefix", raw: []string{"name=cocoon-x,size=1G"}, wantErr: true},
		{name: "duplicate name", raw: []string{"name=x,size=1G", "name=x,size=1G"}, wantErr: true},
		{name: "unknown key", raw: []string{"size=1G,color=red"}, wantErr: true},
		{name: "fstype rejected on macOS", raw: []string{"size=1G,fstype=ext4"}, wantErr: true},
		{name: "mount rejected on macOS", raw: []string{"size=1G,mount=/mnt/x"}, wantErr: true},
		{name: "directio rejected on macOS", raw: []string{"size=1G,directio=on"}, wantErr: true},
		{name: "over the four-disk cap", raw: []string{"size=1G", "size=1G", "size=1G", "size=1G", "size=1G"}, wantErr: true},

		{name: "collides with a reserved name", raw: []string{"name=data1,size=1G"}, reserved: []string{"data1"}, wantErr: true},
		{name: "auto name skips reserved", raw: []string{"size=1G"}, reserved: []string{"data1"}, wantNames: []string{"data2"}, wantSizes: []int64{gib}},
		{name: "reserved fills the cap", raw: []string{"size=1G", "size=1G"}, reserved: []string{"data1", "data2", "data3"}, wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			specs, err := parseDataDisks(tt.raw, tt.reserved)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("want error, got specs %v", specs)
				}
				return
			}
			if err != nil {
				t.Fatalf("parseDataDisks: %v", err)
			}
			gotNames := make([]string, len(specs))
			gotSizes := make([]int64, len(specs))
			for i, s := range specs {
				gotNames[i], gotSizes[i] = s.Name, s.Size
			}
			if !slices.Equal(gotNames, tt.wantNames) {
				t.Errorf("names: got %v, want %v", gotNames, tt.wantNames)
			}
			if !slices.Equal(gotSizes, tt.wantSizes) {
				t.Errorf("sizes: got %v, want %v", gotSizes, tt.wantSizes)
			}
		})
	}
}

func TestDataDiskNameRoundTrip(t *testing.T) {
	for _, name := range []string{"data1", "logs", "a-b_c"} {
		if got := dataDiskName(dataDiskPath("/vm/dir", name)); got != name {
			t.Errorf("dataDiskName(dataDiskPath(%q)) = %q", name, got)
		}
	}
}
