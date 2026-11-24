package nodeops

import (
	"strings"

	"github.com/docker/go-units"
)

// TmpfsCapacity returns only the driver-declared size of a local tmpfs.
func TmpfsCapacity(driver string, options map[string]string) (bool, int64) {
	if driver != "local" || options["type"] != "tmpfs" {
		return false, 0
	}
	for option := range strings.SplitSeq(options["o"], ",") {
		if value, ok := strings.CutPrefix(option, "size="); ok {
			if capacity, err := units.RAMInBytes(value); err == nil && capacity > 0 {
				return true, capacity
			}
		}
	}
	return true, 0
}
