package services

import (
	"context"
	"sort"
	"time"

	"hector/backend/hetzner"
	"hector/backend/types"
)

var catalogCache = newTTLCache[*types.Catalog]()

const catalogTTL = 10 * time.Minute

// invalidateCatalog drops the cached catalog. It carries the image list that
// the New server and Rebuild pickers read, so an image that is created,
// renamed or deleted has to reach those screens now — not whenever the TTL
// happens to run out. Without this a snapshot taken on one server stays
// invisible in the rebuild dialog of every other server for up to ten
// minutes, even though it is already in the Images list.
func invalidateCatalog() { catalogCache.del("catalog") }

// Catalog is everything the New server screen and the Rescale screen need:
// locations, system images, server types with per-location prices and
// availability, and SSH keys.
func Catalog(ctx context.Context) (*types.Catalog, error) {
	if v, ok := catalogCache.get("catalog"); ok {
		return v, nil
	}

	locations, err := hcloud.Locations(ctx)
	if err != nil {
		return nil, err
	}
	typesRaw, err := hcloud.ServerTypes(ctx)
	if err != nil {
		return nil, err
	}
	images, err := hcloud.Images(ctx, "") // all types: system, app, snapshot
	if err != nil {
		return nil, err
	}
	keys, err := hcloud.SSHKeys(ctx)
	if err != nil {
		return nil, err
	}
	isos, _ := hcloud.ISOs(ctx) // ISO list is best-effort
	idx, _ := prices(ctx)

	avail := newAvailabilityIndex(ctx, typesRaw)

	cat := &types.Catalog{
		Currency:      currencyOr(idx, "EUR"),
		BackupPercent: "20",
	}
	if idx != nil {
		if idx.vatRate != "" {
			cat.VatRate = idx.vatRate
		}
		if idx.backup != "" {
			cat.BackupPercent = idx.backup
		}
	}

	for _, l := range locations {
		cat.Locations = append(cat.Locations, types.CatalogLocation{
			Code:    locCode(l.Name),
			Name:    l.Name,
			City:    l.City,
			Country: l.Country,
			Zone:    l.NetworkZone,
		})
	}
	sort.Slice(cat.Locations, func(i, j int) bool { return cat.Locations[i].Code < cat.Locations[j].Code })

	for _, img := range images {
		if img.Status != "available" {
			continue
		}
		cat.Images = append(cat.Images, types.CatalogImage{
			ID:          img.ID,
			Name:        img.Name,
			Description: img.Description,
			Type:        img.Type,
			OSFlavor:    img.OSFlavor,
			OSVersion:   img.OSVersion,
			Arch:        img.Architecture,
		})
	}
	sort.Slice(cat.Images, func(i, j int) bool {
		if cat.Images[i].OSFlavor != cat.Images[j].OSFlavor {
			return cat.Images[i].OSFlavor < cat.Images[j].OSFlavor
		}
		return cat.Images[i].OSVersion > cat.Images[j].OSVersion
	})

	for _, st := range typesRaw {
		entry := types.CatalogServerType{
			ID:         st.ID,
			Name:       st.Name,
			Cores:      st.Cores,
			MemoryGB:   st.Memory,
			DiskGB:     st.Disk,
			Storage:    st.StorageType,
			CPUType:    st.CPUType,
			Arch:       st.Architecture,
			Category:   st.Category,
			Deprecated: st.Deprecated,
			Prices:     map[string]types.TypePrice{},
		}
		for _, p := range st.Prices {
			code := locCode(p.Location)
			entry.Prices[code] = types.TypePrice{
				Monthly:    shownPrice(p.PriceMonthly),
				Hourly:     shownPrice(p.PriceHourly),
				IncludedGB: p.IncludedTraffic >> 30,
				Available:  avail.isAvailable(st, p.Location),
			}
		}
		cat.ServerTypes = append(cat.ServerTypes, entry)
	}
	sort.Slice(cat.ServerTypes, func(i, j int) bool { return cat.ServerTypes[i].Name < cat.ServerTypes[j].Name })

	for _, k := range keys {
		cat.SSHKeys = append(cat.SSHKeys, types.CatalogSSHKey{
			ID:          k.ID,
			Name:        k.Name,
			Fingerprint: k.Fingerprint,
		})
	}

	for _, iso := range isos {
		cat.ISOs = append(cat.ISOs, types.CatalogISO{
			ID:          iso.ID,
			Name:        iso.Name,
			Description: iso.Description,
			Type:        iso.Type,
			Arch:        iso.Architecture,
		})
	}

	// A fresh Hetzner account answers with null for empty collections (e.g.
	// no SSH keys yet); the JSON contract is arrays, so send [] not null —
	// a null sshKeys used to crash the New server screen on `.map`.
	if cat.Locations == nil {
		cat.Locations = []types.CatalogLocation{}
	}
	if cat.Images == nil {
		cat.Images = []types.CatalogImage{}
	}
	if cat.ServerTypes == nil {
		cat.ServerTypes = []types.CatalogServerType{}
	}
	if cat.SSHKeys == nil {
		cat.SSHKeys = []types.CatalogSSHKey{}
	}
	if cat.ISOs == nil {
		cat.ISOs = []types.CatalogISO{}
	}

	catalogCache.set("catalog", cat, catalogTTL)
	return cat, nil
}

// availabilityIndex answers "is this server type sold in this location".
// It is the one place that question is answered, so the New server screen
// and an order-queue stock check can never disagree.
//
// The modern source is the server type's own per-location flags; the
// datacenters endpoint is only fetched when some type does not carry them.
type availabilityIndex struct {
	byID map[string]map[int64]bool  // location name -> type id -> available
	own  map[string]map[string]bool // type name -> location name -> available
}

func newAvailabilityIndex(ctx context.Context, typesRaw []hetzner.ServerType) availabilityIndex {
	idx := availabilityIndex{
		byID: map[string]map[int64]bool{},
		own:  map[string]map[string]bool{},
	}

	needsFallback := false
	for _, st := range typesRaw {
		if len(st.Locations) == 0 {
			needsFallback = true
			continue
		}
		m := make(map[string]bool, len(st.Locations))
		for _, loc := range st.Locations {
			m[loc.Name] = loc.Available
		}
		idx.own[st.Name] = m
	}
	if !needsFallback {
		return idx // one Hetzner request instead of two
	}

	// best-effort, as before: without it an unknown location counts as available
	datacenters, _ := hcloud.Datacenters(ctx)
	for _, dc := range datacenters {
		if dc.ServerTypes == nil {
			continue
		}
		set := idx.byID[dc.Location.Name]
		if set == nil {
			set = map[int64]bool{}
			idx.byID[dc.Location.Name] = set
		}
		for _, id := range dc.ServerTypes.Available {
			set[id] = true
		}
	}
	return idx
}

func (idx availabilityIndex) isAvailable(st hetzner.ServerType, location string) bool {
	if own, ok := idx.own[st.Name]; ok {
		return own[location]
	}
	locSet, hasAvail := idx.byID[location]
	return !hasAvail || locSet[st.ID]
}
