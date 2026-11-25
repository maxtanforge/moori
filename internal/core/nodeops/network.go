package nodeops

func BuiltInNetwork(name string) bool {
	switch name {
	case "bridge", "default", "host", "none", "nat", "ingress", "docker_gwbridge":
		return true
	default:
		return false
	}
}

func ProtectedNetwork(name, scope, driver string, ingress, configOnly bool, options map[string]string) bool {
	return BuiltInNetwork(name) || ingress || configOnly || scope != "local" || driver != "bridge" ||
		options["com.docker.network.bridge.default_bridge"] == "true"
}
