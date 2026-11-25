package executor

import (
	"strconv"
	"time"
)

func composeArgs(plan Plan) []string {
	seconds := (plan.Timeout + time.Second - 1) / time.Second
	pullPolicy := "always"
	if plan.ImagesReady {
		pullPolicy = "never"
	}
	args := []string{"--host", "unix:///var/run/docker.sock",
		"compose", "--env-file", "/dev/null", "-p", plan.Project, "-f", "-",
		"up", "--detach", "--remove-orphans", "--no-build", "--pull", pullPolicy, "--wait", "--wait-timeout", strconv.FormatInt(int64(seconds), 10)}
	if plan.ForceRecreate {
		args = append(args, "--force-recreate")
	}
	return args
}
