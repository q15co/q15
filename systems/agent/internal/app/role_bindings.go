package app

import (
	"log"

	"github.com/q15co/q15/systems/agent/internal/config"
	"github.com/q15co/q15/systems/agent/internal/modelcatalog"
)

func configureModelRoles(cfg config.Config, registry *modelcatalog.Registry) error {
	bindings, err := cfg.ValidateRoles(registry)
	if err != nil {
		return err
	}
	registry.SetRoleBindings(bindings)
	registry.SetRoleValidator(
		func(models []modelcatalog.Model) error { _, err := cfg.ValidateRoleModels(models); return err },
	)
	for _, binding := range bindings {
		if binding.AllowDualBind != "" {
			log.Printf(
				"q15: role %s model=%q allow_dual_bind=%q requests_per_minute=%d request_bytes_per_minute=%d",
				binding.Role,
				binding.Model,
				binding.AllowDualBind,
				binding.RequestsPerMinute,
				binding.RequestBytesPerMinute,
			)
		}
	}

	return nil
}
