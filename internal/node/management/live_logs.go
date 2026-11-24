package management

import (
	"context"
	"io"
)

func (o *Operations) FollowLogs(ctx context.Context, id string, tail int, timestamps, allRuns bool) (io.ReadCloser, error) {
	return o.docker.FollowLogs(ctx, id, tail, timestamps, allRuns)
}
