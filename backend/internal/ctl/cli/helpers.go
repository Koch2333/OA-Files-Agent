package cli

import "oa-agent/backend/internal/ctl/envfile"

func envIsSecret(k string) bool { return envfile.IsSecret(k) }

func validateOnly(k, v string) error { return envfile.ValidateConfig(k, v) }

func shown(k, v string) string {
	if envfile.IsSecret(k) {
		return "********"
	}
	return v
}
