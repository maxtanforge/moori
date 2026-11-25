package main

import (
	"encoding/json"
	"errors"
	"io"
	"os"

	"github.com/maxtanforge/moori/internal/control/integration/gitea"
)

func loadGiteaBindings(path string) ([]gitea.Binding, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	const maxConfig = 128 << 10
	info, err := file.Stat()
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || info.Size() > maxConfig {
		return nil, errors.New("expected a Gitea binding file of at most 128 KiB")
	}
	reader := &io.LimitedReader{R: file, N: maxConfig + 1}
	dec := json.NewDecoder(reader)
	dec.DisallowUnknownFields()
	var bindings []gitea.Binding
	if err := dec.Decode(&bindings); err != nil {
		return nil, errors.New("invalid Gitea binding JSON")
	}
	if err := dec.Decode(new(json.RawMessage)); err != io.EOF || reader.N == 0 {
		return nil, errors.New("expected exactly one Gitea binding array")
	}
	return bindings, nil
}
