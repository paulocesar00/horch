package daemon

import (
	"fmt"
	"github.com/ellingtonsp/herdr-orch/internal/store"
	"strings"
)

func dispatchWorkerMetadata(d store.Dispatch) string {
	parts := []string{}
	for _, field := range []struct{ label, value string }{
		{"agent", d.Agent}, {"account", d.Account}, {"profile", d.Profile}, {"requested model", d.Model},
	} {
		if field.value != "" {
			parts = append(parts, fmt.Sprintf("%s=%q", field.label, field.value))
		}
	}
	if len(parts) == 0 {
		return ""
	}
	return "\nWorker launch: " + strings.Join(parts, ", ") + "."
}
