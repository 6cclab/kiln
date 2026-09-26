package cli

import (
	"fmt"
	"os"
)

// offlineProviders are the providers a run may use under HARNESS_OFFLINE=1:
// the scripted faux server and a local Ollama. Everything else is a paid or
// remote endpoint and is refused, so a scripted or visual test run can never
// spend money or leave the machine by accident (the same guard the eval
// runner applies through KILN_EVAL_LIVE).
var offlineProviders = map[string]bool{"faux": true, "ollama": true}

// offlineGuard refuses providerID when HARNESS_OFFLINE=1 names it as
// non-local.
func offlineGuard(providerID string) error {
	if os.Getenv("HARNESS_OFFLINE") != "1" || offlineProviders[providerID] {
		return nil
	}
	return fmt.Errorf("provider %q refused: HARNESS_OFFLINE=1 allows only faux and ollama", providerID)
}
