package identity

import (
	"errors"
	"os"
	"runtime"
	"strings"

	"github.com/maxtanforge/moori/internal/core/agenttoken"
)

// ReadCredential reads a regular private file, never a URL or command argument.
func ReadCredential(path string) (string, error) {
	data, err := readPrivate(path)
	if err != nil {
		return "", err
	}
	defer clear(data)
	return ParseAgentToken(string(data))
}

// ParseAgentToken validates the same bearer token from a file or the environment.
func ParseAgentToken(value string) (string, error) {
	token := strings.TrimSpace(value)
	if !agenttoken.Valid(token) {
		return "", errors.New("invalid Agent token")
	}
	return token, nil
}

func readPrivate(path string) ([]byte, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || runtime.GOOS != "windows" && info.Mode().Perm()&0077 != 0 || info.Size() > 16<<10 {
		return nil, errors.New("Agent identity must be a private regular file")
	}
	return os.ReadFile(path)
}
