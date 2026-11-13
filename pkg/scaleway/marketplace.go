package scaleway

import (
	"context"
	"errors"
	"fmt"

	marketplace "github.com/scaleway/scaleway-sdk-go/api/marketplace/v2"
	"github.com/scaleway/scaleway-sdk-go/scw"
)

// MarketplaceClient wraps the Scaleway Marketplace API for image-label
// resolution. The reconciler uses LookupImage to round-trip ScalewayNodeClass
// imageLabel values and surface typos as Ready=False/ImageNotFound.
type MarketplaceClient struct {
	api *marketplace.API
}

func NewMarketplaceClient(scwClient *scw.Client) *MarketplaceClient {
	return &MarketplaceClient{api: marketplace.NewAPI(scwClient)}
}

// ErrImageNotFound is returned by LookupImage when the Marketplace API has
// no image registered under the supplied label/zone pair. Distinct from
// transport errors so callers can map to ConditionReasonImageNotFound.
var ErrImageNotFound = errors.New("scaleway: image label not found in zone")

// LookupImage resolves a Marketplace label (e.g. "ubuntu_jammy") to its
// per-zone image UUID. Empty result with ErrImageNotFound on miss.
//
// We deliberately avoid the SDK's GetLocalImageByLabel helper: when no
// image matches, it returns a synthetic *errors.Error wrapping "couldn't
// find a local image..." that's indistinguishable from a 5xx via Go
// types. ListLocalImages returns a clean empty slice on miss, letting us
// distinguish "label doesn't exist" from "API failed".
func (c *MarketplaceClient) LookupImage(ctx context.Context, zone scw.Zone, label string) (string, error) {
	resp, err := c.api.ListLocalImages(&marketplace.ListLocalImagesRequest{
		Zone:       &zone,
		ImageLabel: scw.StringPtr(label),
		Type:       marketplace.LocalImageTypeInstanceLocal,
	}, scw.WithContext(ctx), scw.WithAllPages())
	if err != nil {
		if isNotFound(err) {
			return "", fmt.Errorf("%w (label=%s zone=%s)", ErrImageNotFound, label, zone)
		}
		return "", fmt.Errorf("marketplace lookup label=%s zone=%s: %w", label, zone, err)
	}
	if len(resp.LocalImages) == 0 {
		return "", fmt.Errorf("%w (label=%s zone=%s)", ErrImageNotFound, label, zone)
	}
	// Prefer an image compatible with the default commercial type to
	// surface obviously broken labels (an image registered under the
	// label but incompatible with our catalog is effectively unusable).
	for _, img := range resp.LocalImages {
		if img.IsCompatible("GP1-XS") {
			return img.ID, nil
		}
	}
	// Fall back to the first image — better than failing loudly when the
	// catalog drifts ahead of our hardcoded compatibility check.
	return resp.LocalImages[0].ID, nil
}
