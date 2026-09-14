package vm

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestVMOutputCarriesState(t *testing.T) {
	b, err := json.Marshal(vmOutput{&record{Name: "demo", PID: 4711}, "stopped"})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`"name":"demo"`, `"pid":4711`, `"state":"stopped"`} {
		if !strings.Contains(string(b), want) {
			t.Errorf("json %s lacks %s", b, want)
		}
	}
}
