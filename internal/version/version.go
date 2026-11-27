package version

const (
	Name    = "Master GPU"
	Version = "1.0.10"
)

// Build is injected at link time (-X gpupanel/internal/version.Build=...).
var Build = "dev"
