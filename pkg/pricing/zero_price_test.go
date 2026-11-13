package pricing

import (
	"context"
	"testing"

	"github.com/scaleway/scaleway-sdk-go/api/instance/v1"
	"github.com/scaleway/scaleway-sdk-go/scw"
)

// Seen 2026-11-10: the Instance API ListServersTypes returns hourly_price=0
// for every server type. The refresh used to overwrite the catalog baseline
// with 0, so every offering cost 0 and the cheapest-pick fell back to the
// alphabetically first type (PRO2-S instead of PRO2-XXS on pprod).
func TestProvider_RefreshKeepsBaselineWhenLivePriceIsZero(t *testing.T) {
	baseline, ok := NewProvider(&FakeAPI{}, nil).Price(scw.ZoneFrPar1, "PRO2-XXS")
	if !ok || baseline <= 0 {
		t.Fatalf("precondition: catalog baseline for PRO2-XXS = (%v,%v)", baseline, ok)
	}
	api := &FakeAPI{Data: map[scw.Zone]map[string]*instance.ServerType{
		scw.ZoneFrPar1: {
			"PRO2-XXS": {HourlyPrice: 0},
			"PRO2-S":   {HourlyPrice: 0.3},
		},
	}}
	p := NewProvider(api, nil)
	if err := p.refreshOnce(context.Background()); err != nil {
		t.Fatalf("refresh: %v", err)
	}
	if got, ok := p.Price(scw.ZoneFrPar1, "PRO2-XXS"); !ok || got != baseline {
		t.Fatalf("PRO2-XXS = (%v,%v), want baseline (%v,true)", got, ok, baseline)
	}
	if got, _ := p.Price(scw.ZoneFrPar1, "PRO2-S"); got != 0.3 {
		t.Fatalf("PRO2-S = %v, want live 0.3", got)
	}
}
