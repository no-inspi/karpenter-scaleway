package pricing

import (
	"context"
	"testing"

	pc "github.com/scaleway/scaleway-sdk-go/api/product_catalog/v2alpha1"
	"github.com/scaleway/scaleway-sdk-go/scw"
)

type staticCatalog struct {
	products []*pc.PublicCatalogProduct
	gotZone  scw.Zone
	gotTypes []pc.ListPublicCatalogProductsRequestProductType
}

func (s *staticCatalog) ListPublicCatalogProducts(req *pc.PublicCatalogAPIListPublicCatalogProductsRequest, _ ...scw.RequestOption) (*pc.ListPublicCatalogProductsResponse, error) {
	if req.Zone != nil {
		s.gotZone = *req.Zone
	}
	s.gotTypes = req.ProductTypes
	return &pc.ListPublicCatalogProductsResponse{Products: s.products}, nil
}

func product(offer string, zone scw.Zone, units int64, nanos int32, unit pc.PublicCatalogProductUnitOfMeasureCountableUnit) *pc.PublicCatalogProduct {
	z := zone
	return &pc.PublicCatalogProduct{
		Locality:      &pc.PublicCatalogProductLocality{Zone: &z},
		Price:         &pc.PublicCatalogProductPrice{RetailPrice: &scw.Money{CurrencyCode: "EUR", Units: units, Nanos: nanos}},
		Properties:    &pc.PublicCatalogProductProperties{Instance: &pc.PublicCatalogProductPropertiesInstance{OfferID: offer}},
		UnitOfMeasure: &pc.PublicCatalogProductUnitOfMeasure{Unit: unit, Size: 1},
	}
}

// Shapes taken from the live public catalog on 2026-11-10
// (sku=/instance/server/pro2_xxs/fr-par-1, retail_price=0.0561 EUR/hour).
func TestHourlyPrices(t *testing.T) {
	hour := pc.PublicCatalogProductUnitOfMeasureCountableUnitHour
	api := &staticCatalog{products: []*pc.PublicCatalogProduct{
		product("PRO2-XXS", scw.ZoneFrPar1, 0, 56100000, hour),
		product("pro2_s", scw.ZoneFrPar1, 0, 223380000, hour),                                                // normalized
		product("PRO2-M", scw.ZoneFrPar2, 0, 446760000, hour),                                                // other zone
		product("PRO2-L", scw.ZoneFrPar1, 0, 0, hour),                                                        // zero price
		product("GP1-XS", scw.ZoneFrPar1, 30, 0, pc.PublicCatalogProductUnitOfMeasureCountableUnit("month")), // not hourly
		{Properties: &pc.PublicCatalogProductProperties{}},                                                   // not an instance
		nil,
	}}
	got, err := HourlyPrices(context.Background(), api, scw.ZoneFrPar1)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]float64{"PRO2-XXS": 0.0561, "PRO2-S": 0.22338}
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for k, v := range want {
		if got[k] != v {
			t.Fatalf("%s = %v, want %v (all: %v)", k, got[k], v, got)
		}
	}
	if api.gotZone != scw.ZoneFrPar1 || len(api.gotTypes) != 1 || api.gotTypes[0] != pc.ListPublicCatalogProductsRequestProductTypeInstance {
		t.Fatalf("request zone=%s types=%v", api.gotZone, api.gotTypes)
	}
}
