package service

import "testing"

func TestNativeCoreHelloRequiresPublishedBeta55_2(t *testing.T) {
	valid := nativeCoreHello{Protocol: 1, CorePath: "github.com/batuta-ai/core", CoreVersion: "v1.1.0-beta.55.2", CoreSum: "h1:n8o/2bXJt+4Q0CeNH0vdztoL0Jf6hIoCSH5vStx5EnI=", PID: 1, Created: 1}
	for _, test := range []struct {
		name  string
		alter func(*nativeCoreHello)
		want  bool
	}{
		{"published", func(*nativeCoreHello) {}, true},
		{"legacy49", func(h *nativeCoreHello) {
			h.CoreVersion = "v1.1.0-beta.49"
			h.CoreSum = "h1:GnPVZNM1wbIapKJJoNDc7WQXQNpiWQIWCkkpz1yMWaE="
		}, false},
		{"legacy54", func(h *nativeCoreHello) { h.CoreVersion = "v1.1.0-beta.54" }, false},
		{"legacy55", func(h *nativeCoreHello) {
			h.CoreVersion = "v1.1.0-beta.55"
			h.CoreSum = "h1:dAlI07spnNmDeUZzxX3PnStg+NmkvZFs8UI+B6hH44Y="
		}, false},
		{"legacy55.1", func(h *nativeCoreHello) {
			h.CoreVersion = "v1.1.0-beta.55.1"
			h.CoreSum = "h1:f2QD56y4MNgW4qGrT+xtZRcK9PpWnR4j44E55uAjjO0="
		}, false},
		{"future", func(h *nativeCoreHello) { h.CoreVersion = "v1.1.0-beta.56" }, false},
		{"wrong-sum", func(h *nativeCoreHello) { h.CoreSum = "h1:foreign" }, false},
		{"wrong-path", func(h *nativeCoreHello) { h.CorePath = "example.invalid/core" }, false},
		{"replaced", func(h *nativeCoreHello) { h.CoreReplaced = true }, false},
		{"wrong-protocol", func(h *nativeCoreHello) { h.Protocol = 2 }, false},
		{"missing-pid", func(h *nativeCoreHello) { h.PID = 0 }, false},
		{"missing-time", func(h *nativeCoreHello) { h.Created = 0 }, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			h := valid
			test.alter(&h)
			if got := h.qualified(); got != test.want {
				t.Fatalf("qualified=%t, want %t", got, test.want)
			}
		})
	}
}
