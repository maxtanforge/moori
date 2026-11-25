package nodeops

import (
	"context"
	"errors"
	"strconv"
	"time"

	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/network"
	"github.com/docker/docker/api/types/volume"
	"github.com/docker/docker/errdefs"

	"github.com/maxtanforge/moori/internal/core/nodeops"
)

func (r Runner) volume(ctx context.Context, req nodeops.Request) (nodeops.Result, error) {
	if req.Action == nodeops.VolumeCreate {
		if _, err := r.Docker.VolumeInspect(ctx, req.Name); err == nil {
			return nodeops.Result{}, errors.New("a Docker volume with this name already exists")
		} else if !errdefs.IsNotFound(err) {
			return nodeops.Result{}, dockerError(err)
		}
		options := volume.CreateOptions{Name: req.Name, Driver: "local"}
		if req.TmpfsSizeMiB != 0 {
			options.DriverOpts = map[string]string{"type": "tmpfs", "device": "tmpfs", "o": "size=" + strconv.FormatInt(req.TmpfsSizeMiB, 10) + "m"}
		}
		v, err := r.Docker.VolumeCreate(ctx, options)
		if err != nil {
			return nodeops.Result{}, dockerMutationError(err)
		}
		if v.Name != req.Name {
			return nodeops.Result{}, errors.New("Docker returned a different volume")
		}
		details := volumeDetails(v, nil)
		return nodeops.Result{Volume: &details, ResourceObservedAt: time.Now().UTC().Format(time.RFC3339Nano)}, nil
	}

	v, err := r.Docker.VolumeInspect(ctx, req.Target)
	if err != nil {
		return nodeops.Result{}, dockerError(err)
	}
	if v.Name != req.Target {
		return nodeops.Result{}, errors.New("Docker volume identity changed")
	}
	rows, err := r.Docker.ContainerList(ctx, container.ListOptions{All: true})
	if err != nil {
		return nodeops.Result{}, dockerError(err)
	}
	var refs []nodeops.Reference
	for _, c := range rows {
		for _, m := range c.Mounts {
			if m.Type == "volume" && (m.Name == v.Name || m.Name == "" && v.Mountpoint != "" && m.Source == v.Mountpoint) {
				refs = append(refs, referenceFrom(c))
				break
			}
		}
	}
	if req.Action == nodeops.VolumeDelete {
		if v.Scope != "local" {
			return nodeops.Result{}, errors.New("only local Docker volumes can be deleted on this node")
		}
		if len(refs) > 0 {
			return nodeops.Result{}, errors.New("Docker volume is referenced by a container")
		}
		return observedRemoval(r.Docker.VolumeRemove(ctx, v.Name, false))
	}
	details := volumeDetails(v, refs)
	return nodeops.Result{Volume: &details}, nil
}

func observedRemoval(err error) (nodeops.Result, error) {
	if err != nil {
		return nodeops.Result{}, dockerMutationError(err)
	}
	return nodeops.Result{ResourceObservedAt: time.Now().UTC().Format(time.RFC3339Nano)}, nil
}

func volumeDetails(v volume.Volume, refs []nodeops.Reference) nodeops.Volume {
	if refs == nil {
		refs = []nodeops.Reference{}
	}
	tmpfs, capacity := nodeops.TmpfsCapacity(v.Driver, v.Options)
	return nodeops.Volume{Tmpfs: tmpfs, CapacityBytes: capacity, Name: v.Name, Driver: v.Driver, Scope: v.Scope, Mountpoint: v.Mountpoint, CreatedAt: v.CreatedAt, References: trimReferences(refs), RefCount: len(refs)}
}

func (r Runner) network(ctx context.Context, req nodeops.Request) (nodeops.Result, error) {
	if req.Action == nodeops.NetworkCreate {
		if nodeops.BuiltInNetwork(req.Name) {
			return nodeops.Result{}, errors.New("Docker reserves this network name")
		}
		created, err := r.Docker.NetworkCreate(ctx, req.Name, network.CreateOptions{Driver: "bridge", Scope: "local"})
		if err != nil {
			return nodeops.Result{}, dockerMutationError(err)
		}
		return nodeops.Result{Network: &nodeops.Network{ID: created.ID, Name: req.Name, Driver: "bridge", Scope: "local", References: []nodeops.Reference{}}, ResourceObservedAt: time.Now().UTC().Format(time.RFC3339Nano)}, nil
	}
	n, err := r.Docker.NetworkInspect(ctx, req.Target, network.InspectOptions{})
	if err != nil {
		return nodeops.Result{}, dockerError(err)
	}
	if n.ID != req.Target {
		return nodeops.Result{}, errors.New("Docker network identity changed")
	}
	protected := nodeops.ProtectedNetwork(n.Name, n.Scope, n.Driver, n.Ingress, n.ConfigOnly, n.Options)
	if protected && req.Action != nodeops.NetworkInspect {
		return nodeops.Result{}, errors.New("only user-defined local bridge networks can be changed")
	}
	switch req.Action {
	case nodeops.NetworkConnect, nodeops.NetworkDisconnect:
		if err := r.standaloneContainer(ctx, req.Container); err != nil {
			return nodeops.Result{}, err
		}
		if req.Action == nodeops.NetworkConnect {
			return nodeops.Result{}, dockerMutationError(r.Docker.NetworkConnect(ctx, n.ID, req.Container, nil))
		}
		return nodeops.Result{}, dockerMutationError(r.Docker.NetworkDisconnect(ctx, n.ID, req.Container, false))
	case nodeops.NetworkInspect, nodeops.NetworkDelete:
		refs, err := r.networkReferences(ctx, n)
		if err != nil {
			return nodeops.Result{}, err
		}
		if req.Action == nodeops.NetworkDelete {
			if len(refs) > 0 {
				return nodeops.Result{}, errors.New("Docker network is referenced by a container")
			}
			return observedRemoval(r.Docker.NetworkRemove(ctx, n.ID))
		}
		out := nodeops.Network{ID: n.ID, Name: n.Name, Driver: n.Driver, Scope: n.Scope, Internal: n.Internal, Attachable: n.Attachable, Protected: protected, References: trimReferences(refs), RefCount: len(refs)}
		for _, ip := range n.IPAM.Config {
			if ip.Subnet != "" {
				out.Subnets = append(out.Subnets, ip.Subnet)
			}
			if ip.Gateway != "" {
				out.Gateways = append(out.Gateways, ip.Gateway)
			}
		}
		return nodeops.Result{Network: &out}, nil
	default:
		return nodeops.Result{}, nodeops.ErrInvalid
	}
}

func (r Runner) standaloneContainer(ctx context.Context, id string) error {
	c, err := r.exactContainer(ctx, id)
	if err != nil {
		return err
	}
	if c.Config == nil {
		return errors.New("Docker container configuration is unavailable")
	}
	labels := c.Config.Labels
	for _, label := range []string{"com.docker.compose.project", "com.docker.compose.service", "com.docker.swarm.service.id", "com.docker.stack.namespace"} {
		if _, managed := labels[label]; managed {
			return errors.New("change managed container networks in Compose or the orchestrator")
		}
	}
	return nil
}

func (r Runner) networkReferences(ctx context.Context, n network.Inspect) ([]nodeops.Reference, error) {
	rows, err := r.Docker.ContainerList(ctx, container.ListOptions{All: true})
	if err != nil {
		return nil, dockerError(err)
	}
	refs := make(map[string]nodeops.Reference)
	for id, endpoint := range n.Containers {
		refs[id] = nodeops.Reference{ID: id, Name: endpoint.Name, IPv4Address: endpoint.IPv4Address, IPv6Address: endpoint.IPv6Address}
	}
	for _, c := range rows {
		if ref, ok := refs[c.ID]; ok {
			ref.State = string(c.State)
			refs[c.ID] = ref
			continue
		}
		if c.NetworkSettings == nil {
			continue
		}
		for name, endpoint := range c.NetworkSettings.Networks {
			if endpoint != nil && endpoint.NetworkID == n.ID || name == n.Name && (endpoint == nil || endpoint.NetworkID == "") {
				refs[c.ID] = referenceFrom(c)
				break
			}
		}
	}
	out := make([]nodeops.Reference, 0, len(refs))
	for _, ref := range refs {
		out = append(out, ref)
	}
	return out, nil
}
