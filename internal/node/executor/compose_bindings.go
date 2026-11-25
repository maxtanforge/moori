package executor

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"

	"github.com/maxtanforge/moori/internal/core/release"
)

type inspectResources func(context.Context, string, []string) ([]byte, error)

func checkExternalResources(ctx context.Context, document string, inspect inspectResources) error {
	volumes, networks, err := release.ExternalResources(document)
	if err != nil {
		return err
	}
	if err := checkResources(ctx, "volume", "local", volumes, inspect); err != nil {
		return err
	}
	return checkResources(ctx, "network", "bridge", networks, inspect)
}

func checkResources(ctx context.Context, kind, driver string, names []string, inspect inspectResources) error {
	if len(names) == 0 {
		return nil
	}
	response, err := inspect(ctx, kind, names)
	if err != nil {
		return fmt.Errorf("external Docker %ss must exist on the execution node (%v)", kind, names)
	}
	expected := make(map[string]bool, len(names))
	for _, name := range names {
		expected[name] = true
	}
	decoder := json.NewDecoder(bytes.NewReader(response))
	for {
		var item struct {
			Name    string
			Driver  string
			Scope   string
			Options int
		}
		err := decoder.Decode(&item)
		if err == io.EOF {
			break
		}
		if err != nil || !expected[item.Name] {
			return fmt.Errorf("Docker returned an unexpected external %s record", kind)
		}
		if item.Driver != driver || item.Scope != "local" || item.Options != 0 {
			return fmt.Errorf("external Docker %s %q requires the local %s driver without volume options", kind, item.Name, driver)
		}
		delete(expected, item.Name)
	}
	if len(expected) > 0 {
		return fmt.Errorf("one or more external Docker %ss are missing", kind)
	}
	return nil
}
