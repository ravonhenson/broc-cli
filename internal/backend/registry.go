package backend

import "fmt"

// Factory builds a Backend from raw per-backend config (already decoded into
// a map by the config package).
type Factory func(cfg map[string]any) (Backend, error)

var registry = map[string]Factory{}

// Register makes a backend implementation available by name. Backend
// packages call this from an init() func so adding borg/kopia later only
// requires importing the new package and registering it here.
func Register(name string, f Factory) {
	registry[name] = f
}

// New builds the named backend from its config.
func New(name string, cfg map[string]any) (Backend, error) {
	f, ok := registry[name]
	if !ok {
		return nil, fmt.Errorf("unknown backend %q (available: %v)", name, Names())
	}
	return f(cfg)
}

// Names lists every registered backend.
func Names() []string {
	names := make([]string, 0, len(registry))
	for n := range registry {
		names = append(names, n)
	}
	return names
}
