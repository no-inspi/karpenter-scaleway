package pricing

import (
	"math"
	"sync"

	"github.com/scaleway/scaleway-sdk-go/api/instance/v1"
	pc "github.com/scaleway/scaleway-sdk-go/api/product_catalog/v2alpha1"
	"github.com/scaleway/scaleway-sdk-go/scw"
)

// FakeAPI is a deterministic stand-in for instance.API used in tests.
// It returns ListServersTypesResp and GetServerTypesAvailabilityResponse
// keyed by Zone. Errors take precedence over data when set.
//
// Avail is optional: a zero map means "every type is available" (saves
// boilerplate in tests that don't care about stock).
type FakeAPI struct {
	mu     sync.Mutex
	Data   map[scw.Zone]map[string]*instance.ServerType
	Avail  map[scw.Zone]map[string]instance.ServerTypesAvailability
	ErrFor map[scw.Zone]error
}

func (f *FakeAPI) ListServersTypes(req *instance.ListServersTypesRequest, opts ...scw.RequestOption) (*instance.ListServersTypesResponse, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err, ok := f.ErrFor[req.Zone]; ok && err != nil {
		return nil, err
	}
	servers := f.Data[req.Zone]
	if servers == nil {
		servers = map[string]*instance.ServerType{}
	}
	return &instance.ListServersTypesResponse{Servers: servers, TotalCount: uint32(len(servers))}, nil
}

func (f *FakeAPI) GetServerTypesAvailability(req *instance.GetServerTypesAvailabilityRequest, opts ...scw.RequestOption) (*instance.GetServerTypesAvailabilityResponse, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err, ok := f.ErrFor[req.Zone]; ok && err != nil {
		return nil, err
	}
	zoneAvail := f.Avail[req.Zone]
	out := make(map[string]*instance.GetServerTypesAvailabilityResponseAvailability, len(zoneAvail))
	for name, a := range zoneAvail {
		out[name] = &instance.GetServerTypesAvailabilityResponseAvailability{Availability: a}
	}
	return &instance.GetServerTypesAvailabilityResponse{Servers: out, TotalCount: uint32(len(out))}, nil
}

// ListPublicCatalogProducts serves Data as product catalog entries: one
// hourly-priced instance product per server type of the requested zone,
// priced at ServerType.HourlyPrice (0 means "no price").
func (f *FakeAPI) ListPublicCatalogProducts(req *pc.PublicCatalogAPIListPublicCatalogProductsRequest, opts ...scw.RequestOption) (*pc.ListPublicCatalogProductsResponse, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if req.Zone == nil {
		return &pc.ListPublicCatalogProductsResponse{}, nil
	}
	zone := *req.Zone
	if err, ok := f.ErrFor[zone]; ok && err != nil {
		return nil, err
	}
	out := make([]*pc.PublicCatalogProduct, 0, len(f.Data[zone]))
	for name, st := range f.Data[zone] {
		units := math.Floor(float64(st.HourlyPrice))
		nanos := math.Round((float64(st.HourlyPrice) - units) * 1e9)
		z := zone
		out = append(out, &pc.PublicCatalogProduct{
			Product:       name,
			Locality:      &pc.PublicCatalogProductLocality{Zone: &z},
			Price:         &pc.PublicCatalogProductPrice{RetailPrice: &scw.Money{CurrencyCode: "EUR", Units: int64(units), Nanos: int32(nanos)}},
			Properties:    &pc.PublicCatalogProductProperties{Instance: &pc.PublicCatalogProductPropertiesInstance{OfferID: name}},
			UnitOfMeasure: &pc.PublicCatalogProductUnitOfMeasure{Unit: pc.PublicCatalogProductUnitOfMeasureCountableUnitHour, Size: 1},
		})
	}
	return &pc.ListPublicCatalogProductsResponse{Products: out, TotalCount: uint64(len(out))}, nil
}
