package host

import (
	"runtime"

	"github.com/sctlin/che/internal/config"
)

type Host struct {
	OS            string
	Arch          string
	PackageFormat config.Format
}

func Detect() Host {
	return Host{
		OS:   runtime.GOOS,
		Arch: runtime.GOARCH,
		// TODO: hardcoded rpm for now
		PackageFormat: config.Rpm,
	}

}
