// Package version carries the pairmux build version. The default suits local
// builds; release builds override it with the linker, e.g.
//
//	go build -ldflags "-X github.com/treeleaves30760/pairmux/internal/version.Version=1.2.3"
package version

// Version is the pairmux version string. It is a var (not a const) so a release
// build can inject the real version with -ldflags -X.
var Version = "0.1.0-dev"
