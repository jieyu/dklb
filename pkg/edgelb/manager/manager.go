package manager

import (
	"context"

	httptransport "github.com/go-openapi/runtime/client"
	"github.com/go-openapi/strfmt"
	edgelbclient "github.com/mesosphere/dcos-edge-lb/client"
	edgelboperations "github.com/mesosphere/dcos-edge-lb/client/operations"
	edgelbmodels "github.com/mesosphere/dcos-edge-lb/models"

	"github.com/mesosphere/dklb/pkg/errors"
)

// EdgeLBManagerOptions groups options that can be used to configure an instance of the EdgeLB Manager.
type EdgeLBManagerOptions struct {
	// BearerToken is the (optional) bearer token to use when communicating with the EdgeLB API server.
	BearerToken string
	// Host is the host at which the EdgeLB API server can be reached.
	Host string
	// InsecureSkipTLSVerify indicates whether to skip verification of the TLS certificate presented by the EdgeLB API server.
	InsecureSkipTLSVerify bool
	// Path is the path at which the EdgeLB API server can be reached.
	Path string
	// Scheme is the scheme to use when communicating with the EdgeLB API server.
	Scheme string
}

// EdgeLBManager knows how to manage the configuration of EdgeLB pools.
type EdgeLBManager interface {
	// CreatePool creates the specified EdgeLB pool in the EdgeLB API server.
	CreatePool(context.Context, *edgelbmodels.V2Pool) (*edgelbmodels.V2Pool, error)
	// GetPoolByName returns the EdgeLB pool with the specified name.
	GetPoolByName(context.Context, string) (*edgelbmodels.V2Pool, error)
	// GetVersion returns the current version of EdgeLB.
	GetVersion(context.Context) (string, error)
}

// edgeLBManager is the main implementation of the EdgeLB manager.
// TODO (@bcustodio) Figure out a way to test this.
type edgeLBManager struct {
	// client is a client for the EdgeLB API server.
	client *edgelbclient.DcosEdgeLb
}

// NewEdgeLBManager creates a new instance of EdgeLBManager configured according to the provided options.
func NewEdgeLBManager(opts EdgeLBManagerOptions) *edgeLBManager {
	var (
		t *httptransport.Runtime
	)

	if !opts.InsecureSkipTLSVerify {
		// Use the default HTTP client, which does not skip TLS verification.
		t = httptransport.New(opts.Host, opts.Path, []string{opts.Scheme})
	} else {
		// Create an HTTP client that skips TLS verification.
		c, err := httptransport.TLSClient(httptransport.TLSClientOptions{
			InsecureSkipVerify: true,
		})
		if err != nil {
			panic(err)
		}
		t = httptransport.NewWithClient(opts.Host, opts.Path, []string{opts.Scheme}, c)
	}
	if opts.BearerToken != "" {
		// Use the specified bearer token for authentication.
		t.DefaultAuthentication = httptransport.BearerToken(opts.BearerToken)
	}

	return &edgeLBManager{
		client: edgelbclient.New(t, strfmt.Default),
	}
}

// CreatePool creates the specified EdgeLB pool in the EdgeLB API server.
func (m *edgeLBManager) CreatePool(ctx context.Context, pool *edgelbmodels.V2Pool) (*edgelbmodels.V2Pool, error) {
	p := &edgelboperations.V2CreatePoolParams{
		Context: ctx,
		Pool:    pool,
	}
	r, err := m.client.Operations.V2CreatePool(p)
	if err == nil {
		return r.Payload, nil
	}
	return nil, errors.Unknown(err)
}

// GetPoolByName returns the EdgeLB pool with the specified name.
func (m *edgeLBManager) GetPoolByName(ctx context.Context, name string) (*edgelbmodels.V2Pool, error) {
	p := &edgelboperations.V2GetPoolParams{
		Context: ctx,
		Name:    name,
	}
	r, err := m.client.Operations.V2GetPool(p)
	if err == nil {
		return r.Payload, nil
	}
	if err, ok := err.(*edgelboperations.V2GetPoolDefault); ok && err.Code() == 404 {
		return nil, errors.NotFound(err)
	} else {
		return nil, errors.Unknown(err)
	}
}

// GetVersion returns the current version of EdgeLB.
func (m *edgeLBManager) GetVersion(ctx context.Context) (string, error) {
	r, err := m.client.Operations.Version(edgelboperations.NewVersionParamsWithContext(ctx))
	if err != nil {
		return "", errors.Unknown(err)
	}
	return r.Payload, nil
}