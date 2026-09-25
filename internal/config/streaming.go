package config

import (
	"encoding/json"
	"fmt"
	"time"
)

// JSON uses the same human-readable durations as YAML; no nanosecond integers
// in admin forms or persisted policy. A nil deployment policy inherits all
// server values. Within a policy, zero timeouts inherit and zero keepalive
// disables comments, allowing one deployment to opt out of a global keepalive.
func (s StreamingConfig) MarshalJSON() ([]byte, error) {
	return json.Marshal(map[string]string{
		"first_event_timeout": s.FirstEventTimeout.String(),
		"idle_timeout":        s.IdleTimeout.String(),
		"write_timeout":       s.WriteTimeout.String(),
		"keepalive_interval":  s.KeepaliveInterval.String(),
	})
}

func (s *StreamingConfig) UnmarshalJSON(data []byte) error {
	var raw map[string]string
	if err := json.Unmarshal(data, &raw); err != nil {
		return fmt.Errorf("streaming durations must be strings: %w", err)
	}
	var out StreamingConfig
	fields := map[string]*time.Duration{"first_event_timeout": &out.FirstEventTimeout, "idle_timeout": &out.IdleTimeout, "write_timeout": &out.WriteTimeout, "keepalive_interval": &out.KeepaliveInterval}
	for name, value := range raw {
		target, ok := fields[name]
		if !ok {
			return fmt.Errorf("unknown streaming setting %q", name)
		}
		duration, err := time.ParseDuration(value)
		if err != nil {
			return fmt.Errorf("streaming.%s: %w", name, err)
		}
		*target = duration
	}
	if err := out.Validate(); err != nil {
		return err
	}
	*s = out
	return nil
}

func (s StreamingConfig) Validate() error {
	for name, value := range map[string]time.Duration{"first_event_timeout": s.FirstEventTimeout, "idle_timeout": s.IdleTimeout, "write_timeout": s.WriteTimeout, "keepalive_interval": s.KeepaliveInterval} {
		if value < 0 || value > 24*time.Hour {
			return fmt.Errorf("streaming.%s must be between zero and 24h", name)
		}
	}
	if s.KeepaliveInterval > 0 && s.KeepaliveInterval < 10*time.Millisecond {
		return fmt.Errorf("streaming.keepalive_interval must be zero or at least 10ms")
	}
	return nil
}

func (s StreamingConfig) WithOverride(override *StreamingConfig) StreamingConfig {
	s = s.WithDefaults()
	if override == nil {
		return s
	}
	if override.FirstEventTimeout > 0 {
		s.FirstEventTimeout = override.FirstEventTimeout
	}
	if override.IdleTimeout > 0 {
		s.IdleTimeout = override.IdleTimeout
	}
	if override.WriteTimeout > 0 {
		s.WriteTimeout = override.WriteTimeout
	}
	s.KeepaliveInterval = override.KeepaliveInterval
	return s
}
