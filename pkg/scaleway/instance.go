package scaleway

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"

	instance "github.com/scaleway/scaleway-sdk-go/api/instance/v1"
	"github.com/scaleway/scaleway-sdk-go/scw"
)

const TagKarpenterManaged = "karpenter-managed"

// Default values used when the corresponding spec field is unset on the
// ScalewayNodeClass. They preserve the spike's behavior and the Scaleway
// service defaults.
const (
	DefaultImageLabel = "ubuntu_jammy"
)

type Client struct {
	api         *instance.API
	poolID      string
	apiKey      string
	k8sAgentURL string
}

type ScalewayInstance struct {
	ID        string
	Name      string
	Type      string
	Zone      scw.Zone
	State     instance.ServerState
	PrivateIP string
	PublicIP  string // first PublicIPs[0].Address; populated after PowerOn
	NodeClaim string // value of the "nodeclaim=" tag, if present
}

// CreateInstanceParams carries the fields a caller needs to provision a
// Kosmos worker Instance. Required: Zone, InstanceType, Name. The remaining
// fields default to spike-equivalent values when zero.
type CreateInstanceParams struct {
	// Zone is the Scaleway zone the Instance will be provisioned in.
	Zone scw.Zone
	// InstanceType is the Scaleway commercial type (e.g. "GP1-XS").
	InstanceType string
	// Name is the Scaleway server name (also stored in the nodeclaim= tag).
	Name string
	// ImageLabel is the Marketplace image label or UUID. Empty → DefaultImageLabel.
	ImageLabel string
	// Tags are merged with the well-known karpenter tags.
	Tags []string
	// RootVolumeSizeGB overrides the root volume size when non-nil.
	RootVolumeSizeGB *int32
	// RootVolumeType is one of l_ssd, sbs_volume. Empty → Scaleway default.
	RootVolumeType string
	// PublicIP forces dynamic IP allocation. Nil → Scaleway default (true),
	// false → no dynamic IP. The k8s-agent registration requires Internet
	// reachability so PublicIP=false is unsupported.
	PublicIP *bool
}

func NewClient(scwClient *scw.Client, poolID, apiKey, k8sAgentURL string) *Client {
	return &Client{
		api:         instance.NewAPI(scwClient),
		poolID:      poolID,
		apiKey:      apiKey,
		k8sAgentURL: k8sAgentURL,
	}
}

// Create provisions a Kosmos worker Instance using the supplied params,
// uploads the cloud-init script that registers the node with the Kosmos
// pool, and powers on the server. On any error, the partially-created
// Instance is terminated (best effort) so the latency measurements stay
// trustworthy.
func (c *Client) Create(ctx context.Context, params CreateInstanceParams) (*ScalewayInstance, error) {
	if params.Zone == "" {
		return nil, errors.New("instance: CreateInstanceParams.Zone is required")
	}
	if params.InstanceType == "" || params.Name == "" {
		return nil, fmt.Errorf("scaleway: InstanceType and Name are required")
	}
	imageLabel := params.ImageLabel
	if imageLabel == "" {
		imageLabel = DefaultImageLabel
	}

	tags := append([]string{TagKarpenterManaged, nodeClaimTagPrefix + params.Name}, params.Tags...)

	req := &instance.CreateServerRequest{
		Zone:           params.Zone,
		Name:           params.Name,
		CommercialType: params.InstanceType,
		Image:          scw.StringPtr(imageLabel),
		Tags:           tags,
	}

	if params.RootVolumeSizeGB != nil || params.RootVolumeType != "" {
		req.Volumes = map[string]*instance.VolumeServerTemplate{
			"0": rootVolumeTemplate(params.RootVolumeSizeGB, params.RootVolumeType),
		}
	}
	if params.PublicIP != nil {
		req.DynamicIPRequired = params.PublicIP
	}

	srv, err := c.api.CreateServer(req, scw.WithContext(ctx))
	if err != nil {
		return nil, fmt.Errorf("create server: %w", err)
	}
	instanceID := srv.Server.ID

	created := false
	defer func() {
		if !created {
			// Cleanup uses context.Background() so it survives ctx cancellation.
			// Logging the orphan keeps it visible in operations dashboards
			// even though we swallow the error itself.
			if err := c.Delete(context.Background(), params.Zone, instanceID); err != nil {
				fmt.Fprintf(os.Stderr, "scaleway: cleanup of orphaned instance %s failed: %v\n", instanceID, err)
			}
		}
	}()

	region, err := params.Zone.Region()
	if err != nil {
		return nil, fmt.Errorf("derive region from zone %q: %w", params.Zone, err)
	}
	cloudInit, err := RenderCloudInit(CloudInitParams{
		PoolID:      c.poolID,
		PoolRegion:  region.String(),
		APIKey:      c.apiKey,
		K8sAgentURL: c.k8sAgentURL,
	})
	if err != nil {
		return nil, fmt.Errorf("render cloud-init: %w", err)
	}
	err = c.api.SetServerUserData(&instance.SetServerUserDataRequest{
		Zone:     params.Zone,
		ServerID: instanceID,
		Key:      "cloud-init",
		Content:  bytes.NewReader([]byte(cloudInit)),
	}, scw.WithContext(ctx))
	if err != nil {
		return nil, fmt.Errorf("set user-data: %w", err)
	}

	if err := c.api.ServerActionAndWait(&instance.ServerActionAndWaitRequest{
		Zone:     params.Zone,
		ServerID: instanceID,
		Action:   instance.ServerActionPoweron,
	}, scw.WithContext(ctx)); err != nil {
		return nil, fmt.Errorf("poweron: %w", err)
	}

	// Refresh after PowerOn — the CreateServer response does not always
	// carry the PublicIP we need to correlate with the kube Node later.
	fresh, err := c.api.GetServer(&instance.GetServerRequest{
		Zone:     params.Zone,
		ServerID: instanceID,
	}, scw.WithContext(ctx))
	if err != nil {
		return nil, fmt.Errorf("get server after poweron: %w", err)
	}

	created = true
	return toScalewayInstance(fresh.Server, params.Zone), nil
}

func rootVolumeTemplate(sizeGB *int32, volumeType string) *instance.VolumeServerTemplate {
	tmpl := &instance.VolumeServerTemplate{
		Boot: scw.BoolPtr(true),
	}
	if sizeGB != nil {
		// Scaleway expects the size in bytes, not gigabytes.
		size := scw.Size(*sizeGB) * scw.GB
		tmpl.Size = &size
	}
	if volumeType != "" {
		tmpl.VolumeType = instance.VolumeVolumeType(volumeType)
	}
	return tmpl
}

func (c *Client) Delete(ctx context.Context, zone scw.Zone, instanceID string) error {
	err := c.api.ServerActionAndWait(&instance.ServerActionAndWaitRequest{
		Zone:     zone,
		ServerID: instanceID,
		Action:   instance.ServerActionTerminate,
	}, scw.WithContext(ctx))
	if err != nil {
		if isNotFound(err) {
			return nil
		}
		return fmt.Errorf("terminate: %w", err)
	}
	return nil
}

func (c *Client) Get(ctx context.Context, zone scw.Zone, instanceID string) (*ScalewayInstance, error) {
	resp, err := c.api.GetServer(&instance.GetServerRequest{
		Zone:     zone,
		ServerID: instanceID,
	}, scw.WithContext(ctx))
	if err != nil {
		return nil, err
	}
	return toScalewayInstance(resp.Server, zone), nil
}

// List enumerates karpenter-managed instances in every Scaleway zone for the
// pool this client targets. Results across zones are concatenated.
func (c *Client) List(ctx context.Context) ([]*ScalewayInstance, error) {
	var out []*ScalewayInstance
	for _, zone := range AllZones {
		resp, err := c.api.ListServers(&instance.ListServersRequest{
			Zone: zone,
			Tags: []string{TagKarpenterManaged},
		}, scw.WithContext(ctx), scw.WithAllPages())
		if err != nil {
			return nil, fmt.Errorf("list servers in %s: %w", zone, err)
		}
		for _, s := range resp.Servers {
			out = append(out, toScalewayInstance(s, zone))
		}
	}
	return out, nil
}

func toScalewayInstance(s *instance.Server, zone scw.Zone) *ScalewayInstance {
	return &ScalewayInstance{
		ID:        s.ID,
		Name:      s.Name,
		Type:      s.CommercialType,
		Zone:      zone,
		State:     s.State,
		PrivateIP: derefStr(s.PrivateIP),
		PublicIP:  firstPublicIP(s),
		NodeClaim: nodeClaimFromTags(s.Tags),
	}
}

func firstPublicIP(s *instance.Server) string {
	if len(s.PublicIPs) > 0 && s.PublicIPs[0] != nil && s.PublicIPs[0].Address != nil {
		return s.PublicIPs[0].Address.String()
	}
	//nolint:staticcheck // PublicIP deprecated in favor of PublicIPs; fallback for legacy API responses, drop when SDK retires the field
	if s.PublicIP != nil && s.PublicIP.Address != nil {
		return s.PublicIP.Address.String() //nolint:staticcheck
	}
	return ""
}

const nodeClaimTagPrefix = "nodeclaim="

func nodeClaimFromTags(tags []string) string {
	for _, t := range tags {
		if len(t) > len(nodeClaimTagPrefix) && t[:len(nodeClaimTagPrefix)] == nodeClaimTagPrefix {
			return t[len(nodeClaimTagPrefix):]
		}
	}
	return ""
}

// IsNotFound reports whether err corresponds to a Scaleway 404 (either the
// SDK's typed ResourceNotFoundError or a raw ResponseError with status 404).
// Exported so backends can keep IsDrifted/Get pure of SDK error matching.
func IsNotFound(err error) bool {
	var rerr *scw.ResponseError
	if errors.As(err, &rerr) && rerr.StatusCode == 404 {
		return true
	}
	var nfErr *scw.ResourceNotFoundError
	return errors.As(err, &nfErr)
}

// isNotFound is the internal alias kept for unchanged callers in this
// package. New code should use IsNotFound.
func isNotFound(err error) bool { return IsNotFound(err) }

func derefStr(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}
