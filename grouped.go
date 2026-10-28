package talia

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
)

// groupedDomainKey compares valid domains by normalized identity without
// rewriting stored records or collapsing distinct invalid domains into one key.
func groupedDomainKey(raw string) string {
	if normalized := normalizeDomain(raw); normalized != "" {
		return normalized
	}
	return raw
}

// mergeGrouped merges new grouped results into existing grouped data, deduplicating by domain.
// The newest result for a domain wins. Existing entries keep their position when re-checked
// into the same bucket; new domains, and domains that change bucket, are appended in input
// order, so repeated writes produce stable output.
func mergeGrouped(existing, newest GroupedData) GroupedData {
	// latest holds the newest entry per domain and which bucket it belongs in.
	type bucketedEntry struct {
		rec       GroupedDomain
		available bool
	}
	latest := make(map[string]bucketedEntry)
	for _, gd := range existing.Available {
		latest[groupedDomainKey(gd.Domain)] = bucketedEntry{gd, true}
	}
	for _, gd := range existing.Unavailable {
		latest[groupedDomainKey(gd.Domain)] = bucketedEntry{gd, false}
	}
	for _, gd := range newest.Available {
		latest[groupedDomainKey(gd.Domain)] = bucketedEntry{gd, true}
	}
	for _, gd := range newest.Unavailable {
		latest[groupedDomainKey(gd.Domain)] = bucketedEntry{gd, false}
	}

	// Emit domains in order of first appearance within their final bucket.
	out := GroupedData{}
	emitted := make(map[string]bool)
	emit := func(gd GroupedDomain, available bool) {
		key := groupedDomainKey(gd.Domain)
		r := latest[key]
		if emitted[key] || r.available != available {
			return
		}
		emitted[key] = true
		if available {
			out.Available = append(out.Available, r.rec)
		} else {
			out.Unavailable = append(out.Unavailable, r.rec)
		}
	}
	for _, gd := range existing.Available {
		emit(gd, true)
	}
	for _, gd := range newest.Available {
		emit(gd, true)
	}
	for _, gd := range existing.Unavailable {
		emit(gd, false)
	}
	for _, gd := range newest.Unavailable {
		emit(gd, false)
	}
	return out
}

// ConvertArrayToGrouped turns an array of DomainRecord into GroupedData.
func ConvertArrayToGrouped(arr []DomainRecord) GroupedData {
	var gd GroupedData
	for _, rec := range arr {
		gDom := GroupedDomain{
			Domain: rec.Domain,
			Reason: rec.Reason,
			Log:    rec.Log,
		}
		if rec.Available {
			gd.Available = append(gd.Available, gDom)
		} else {
			gd.Unavailable = append(gd.Unavailable, gDom)
		}
	}
	return gd
}

// groupedFile is the on-disk shape written by WriteGroupedFile: both result buckets are
// always present, and unverified only when it still has domains waiting to be checked.
type groupedFile struct {
	GroupedData
	Unverified []DomainRecord `json:"unverified,omitempty"`
}

// WriteGroupedFile reads an existing grouped JSON (if any), merges new data, and writes back.
// If the existing file is an array (plain DomainRecord[]), we convert it to grouped before merging.
// Unverified domains in the existing file are kept, except those that newest now has a result for.
func WriteGroupedFile(path string, newest GroupedData) error {
	if path == "" {
		return nil
	}

	file, err := readDomainFile(path, true)
	if err != nil {
		var pathErr *os.PathError
		if errors.As(err, &pathErr) {
			return fmt.Errorf("read grouped file: %w", err)
		}
		return fmt.Errorf("parse grouped file: %w", err)
	}
	data := applyGroupedChecks(file.asGrouped(), newest)
	merged := groupedFile{
		GroupedData: GroupedData{Available: data.Available, Unavailable: data.Unavailable},
		Unverified:  data.Unverified,
	}

	out, err := json.MarshalIndent(merged, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal grouped data: %w", err)
	}
	if err := os.WriteFile(path, out, 0644); err != nil {
		return fmt.Errorf("write grouped file: %w", err)
	}
	return nil
}

// applyGroupedChecks owns newest-wins transitions for both in-place and separate
// grouped files. Only fresh checks remove domains from the unverified bucket.
func applyGroupedChecks(existing ExtendedGroupedData, newest GroupedData) ExtendedGroupedData {
	checked := make(map[string]bool)
	for _, bucket := range [][]GroupedDomain{newest.Available, newest.Unavailable} {
		for _, rec := range bucket {
			checked[groupedDomainKey(rec.Domain)] = true
		}
	}
	merged := mergeGrouped(GroupedData{Available: existing.Available, Unavailable: existing.Unavailable}, newest)
	out := ExtendedGroupedData{Available: merged.Available, Unavailable: merged.Unavailable}
	for _, rec := range existing.Unverified {
		if !checked[groupedDomainKey(rec.Domain)] {
			out.Unverified = append(out.Unverified, rec)
		}
	}
	return out
}
