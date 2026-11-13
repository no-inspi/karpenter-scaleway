package pricing

import (
	"context"
	"math"

	"github.com/scaleway/scaleway-sdk-go/api/instance/v1"
	pc "github.com/scaleway/scaleway-sdk-go/api/product_catalog/v2alpha1"
	"github.com/scaleway/scaleway-sdk-go/scw"

	"github.com/no-inspi/karpenter-scaleway/pkg/scaleway"
)

// CatalogAPI is the subset of the Scaleway Product Catalog API used for
// prices. Since 2026-11 the Instance API returns hourly_price=0 for every
// server type; the public product catalog is the source of truth.
type CatalogAPI interface {
	ListPublicCatalogProducts(*pc.PublicCatalogAPIListPublicCatalogProductsRequest, ...scw.RequestOption) (*pc.ListPublicCatalogProductsResponse, error)
}

// scalewayAPI combines the Instance API (stock availability) and the
// public Product Catalog API (prices) behind the Provider's API interface.
type scalewayAPI struct {
	instance *instance.API
	catalog  *pc.PublicCatalogAPI
}

// NewScalewayAPI returns the production API for NewProvider.
func NewScalewayAPI(client *scw.Client) API {
	return &scalewayAPI{instance: instance.NewAPI(client), catalog: pc.NewPublicCatalogAPI(client)}
}

func (a *scalewayAPI) GetServerTypesAvailability(req *instance.GetServerTypesAvailabilityRequest, opts ...scw.RequestOption) (*instance.GetServerTypesAvailabilityResponse, error) {
	return a.instance.GetServerTypesAvailability(req, opts...)
}

func (a *scalewayAPI) ListPublicCatalogProducts(req *pc.PublicCatalogAPIListPublicCatalogProductsRequest, opts ...scw.RequestOption) (*pc.ListPublicCatalogProductsResponse, error) {
	return a.catalog.ListPublicCatalogProducts(req, opts...)
}

// HourlyPrices returns the EUR/h retail price of every Instance server
// type sold in zone, keyed by the catalog server type name ("PRO2-XXS").
// Products without an hourly price, with a non-positive price or located
// in another zone are skipped.
func HourlyPrices(ctx context.Context, api CatalogAPI, zone scw.Zone) (map[string]float64, error) {
	z := zone
	resp, err := api.ListPublicCatalogProducts(&pc.PublicCatalogAPIListPublicCatalogProductsRequest{
		ProductTypes: []pc.ListPublicCatalogProductsRequestProductType{pc.ListPublicCatalogProductsRequestProductTypeInstance},
		Zone:         &z,
	}, scw.WithContext(ctx), scw.WithAllPages())
	if err != nil {
		return nil, err
	}
	out := make(map[string]float64, len(resp.Products))
	for _, p := range resp.Products {
		if p == nil || p.Properties == nil || p.Properties.Instance == nil || p.Properties.Instance.OfferID == "" {
			continue
		}
		if p.Locality != nil && p.Locality.Zone != nil && *p.Locality.Zone != zone {
			continue
		}
		if p.UnitOfMeasure != nil && (p.UnitOfMeasure.Unit != pc.PublicCatalogProductUnitOfMeasureCountableUnitHour || p.UnitOfMeasure.Size != 1) {
			continue
		}
		if p.Price == nil || p.Price.RetailPrice == nil {
			continue
		}
		v := moneyToEUR(p.Price.RetailPrice)
		if v <= 0 {
			continue
		}
		out[scaleway.NormalizeNodeType(p.Properties.Instance.OfferID)] = v
	}
	return out, nil
}

// moneyToEUR converts scw.Money to a float rounded to 6 decimals (same
// precision as RoundEURHourly).
func moneyToEUR(m *scw.Money) float64 {
	v := float64(m.Units) + float64(m.Nanos)/1e9
	return math.Round(v*1e6) / 1e6
}
