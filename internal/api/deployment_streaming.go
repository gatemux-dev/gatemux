package api

import "github.com/gatemux-dev/gatemux/internal/config"

func (h *V1Handler) streamingFor(name string) config.StreamingConfig {
	if h.Router != nil {
		if deployment := h.Router.Deployment(name); deployment != nil {
			return h.Streaming.WithOverride(deployment.Streaming)
		}
	}
	return h.Streaming.WithDefaults()
}
