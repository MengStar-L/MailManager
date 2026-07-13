package version

var (
	Version     = "dev"
	Commit      = "unknown"
	BuildTime   = "unknown"
	BuildMarker = "mailmanager-release:dev:unknown:unknown"
)

func MarkerFor(version, goos, goarch string) string {
	return "mailmanager-release:" + version + ":" + goos + ":" + goarch
}
