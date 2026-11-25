package release

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"maps"
	"strings"
	"time"
)

// NodeIdentity identifies the Agent generation that owns external Docker resources.
type NodeIdentity struct {
	Agent      string `json:"agent"`
	Generation int64  `json:"generation"`
}

func (n NodeIdentity) Valid() bool {
	return n.Agent != "" && len(n.Agent) <= 128 &&
		strings.TrimSpace(n.Agent) == n.Agent && n.Generation > 0
}

// Binding contains per-target configuration identity and secret version
// references. It must never contain resolved secret values.
type Binding struct {
	Target, ConfigVersion string
	HealthTimeout         time.Duration
	ForceRecreate         bool
	Secrets               map[string]string
	SecretServices        map[string][]string
	ExternalNode          *NodeIdentity
}

type Snapshot struct {
	document string
	digest   string
	target   string
	release  string
	secrets  map[string]string
	external bool
	node     NodeIdentity
}

type snapshotDocument struct {
	ForceRecreate   bool                `json:"force_recreate,omitempty"`
	Schema          int                 `json:"schema"`
	Target          string              `json:"target"`
	ConfigVersion   string              `json:"config_version"`
	HealthTimeoutNS int64               `json:"health_timeout_ns"`
	ReleaseDigest   string              `json:"release_digest"`
	Compose         json.RawMessage     `json:"compose"`
	Secrets         map[string]string   `json:"secrets,omitempty"`
	SecretServices  map[string][]string `json:"secret_services,omitempty"`
	ExternalNode    *NodeIdentity       `json:"external_node,omitempty"`
}

func (s Snapshot) JSON() string                        { return s.document }
func (s Snapshot) Digest() string                      { return s.digest }
func (s Snapshot) Target() string                      { return s.target }
func (s Snapshot) ReleaseDigest() string               { return s.release }
func (s Snapshot) SecretReferences() map[string]string { return maps.Clone(s.secrets) }

func (s Snapshot) UsesExternalResources() bool { return s.external }
func (s Snapshot) ExternalNode() NodeIdentity  { return s.node }

func (r Release) Bind(b Binding) (Snapshot, error) {
	if r.digest == "" || b.Target == "" || b.ConfigVersion == "" || b.HealthTimeout <= 0 || b.HealthTimeout > 30*time.Minute {
		return Snapshot{}, errors.New("release, target, configuration version and health timeout up to 30 minutes are required")
	}
	// Legacy snapshots without a node remain readable for history and reconciliation.
	var node NodeIdentity
	if b.ExternalNode != nil {
		node = *b.ExternalNode
		if !node.Valid() {
			return Snapshot{}, errors.New("deployment requires a valid Agent identity")
		}
	}
	secrets, err := (SecretBindings{Secrets: b.Secrets, Services: b.SecretServices}).Canonical()
	if err != nil {
		return Snapshot{}, err
	}
	if err := r.validateSecretServices(secrets.Services); err != nil {
		return Snapshot{}, err
	}
	input := snapshotDocument{
		Schema: 1, ForceRecreate: b.ForceRecreate, Target: b.Target, ConfigVersion: b.ConfigVersion, HealthTimeoutNS: int64(b.HealthTimeout),
		ReleaseDigest: r.digest, Compose: json.RawMessage(r.document), Secrets: secrets.Secrets, SecretServices: secrets.Services,
	}
	if b.ExternalNode != nil {
		input.ExternalNode = &node
	}
	encoded, err := json.Marshal(input)
	if err != nil {
		return Snapshot{}, err
	}
	if len(encoded) > 2*maxComposeBytes {
		return Snapshot{}, errors.New("execution snapshot exceeds 512 KiB")
	}
	return Snapshot{
		document: string(encoded), digest: hash(encoded), target: b.Target, release: r.digest,
		secrets: maps.Clone(secrets.Secrets), external: r.external, node: node,
	}, nil
}

type Verified struct {
	Target, Compose string
	HealthTimeout   time.Duration
	ForceRecreate   bool
	Secrets         map[string]string
	SecretServices  map[string][]string
}

// Verify checks the wire bytes against the accepted digest, then reapplies
// Compose validation locally without resolving tags or contacting a registry.
func Verify(ctx context.Context, document, digest string) (Verified, error) {
	_, view, err := decodeSnapshot(ctx, document, digest)
	return view, err
}

// ParseSnapshot recovers the immutable typed value from a persisted document.
func ParseSnapshot(ctx context.Context, document, digest string) (Snapshot, error) {
	snapshot, _, err := decodeSnapshot(ctx, document, digest)
	return snapshot, err
}

func decodeSnapshot(ctx context.Context, document, digest string) (Snapshot, Verified, error) {
	if len(document) > 2*maxComposeBytes || hash([]byte(document)) != digest {
		return Snapshot{}, Verified{}, errors.New("snapshot digest mismatch or size limit exceeded")
	}
	var input snapshotDocument
	if err := json.Unmarshal([]byte(document), &input); err != nil {
		return Snapshot{}, Verified{}, err
	}
	r, err := Compile(ctx, input.Compose, nil, nil, nil)
	if err != nil {
		return Snapshot{}, Verified{}, err
	}
	timeout := time.Duration(input.HealthTimeoutNS)
	canonical, err := r.Bind(Binding{Target: input.Target, ConfigVersion: input.ConfigVersion, HealthTimeout: timeout, ForceRecreate: input.ForceRecreate, Secrets: input.Secrets, SecretServices: input.SecretServices, ExternalNode: input.ExternalNode})
	if err != nil {
		return Snapshot{}, Verified{}, err
	}
	if canonical.JSON() != document {
		return Snapshot{}, Verified{}, errors.New("snapshot is not canonical")
	}
	return canonical, Verified{Target: input.Target, Compose: r.Compose(), HealthTimeout: timeout, ForceRecreate: input.ForceRecreate, Secrets: input.Secrets, SecretServices: input.SecretServices}, nil
}

func hash(data []byte) string {
	sum := sha256.Sum256(data)
	return "sha256:" + hex.EncodeToString(sum[:])
}
