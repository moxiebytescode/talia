package talia

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"regexp"
	"strings"
)

var validDomainLabel = regexp.MustCompile(`^[a-z0-9](?:[a-z0-9-]*[a-z0-9])?$`)

// domainFile preserves the input shape for in-place checks and cleaning.
type domainFile struct {
	array   []DomainRecord
	grouped ExtendedGroupedData
	isArray bool
}

// readDomainFile allows missing or empty files only when preparing an update.
// Every other read or decoding error is returned before callers can overwrite data.
func readDomainFile(path string, allowEmpty bool) (domainFile, error) {
	raw, err := os.ReadFile(path)
	if allowEmpty && errors.Is(err, os.ErrNotExist) {
		return domainFile{}, nil
	}
	if err != nil {
		return domainFile{}, fmt.Errorf("reading %s: %w", path, err)
	}
	if allowEmpty && len(bytes.TrimSpace(raw)) == 0 {
		return domainFile{}, nil
	}
	data, err := decodeDomainFile(raw)
	if err != nil {
		return domainFile{}, fmt.Errorf("parsing %s: %w", path, err)
	}
	return data, nil
}

func decodeDomainFile(raw []byte) (domainFile, error) {
	var data domainFile
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 {
		return data, fmt.Errorf("expected an array file or grouped file")
	}
	switch raw[0] {
	case '[':
		data.isArray = true
		if err := json.Unmarshal(raw, &data.array); err != nil {
			return data, err
		}
	case '{':
		if err := json.Unmarshal(raw, &data.grouped); err != nil {
			return data, err
		}
		var fields map[string]json.RawMessage
		_ = json.Unmarshal(raw, &fields) // the typed decode above already checked the JSON
		if len(fields) > 0 && fields["available"] == nil && fields["unavailable"] == nil && fields["unverified"] == nil {
			return data, fmt.Errorf("expected available, unavailable, or unverified buckets")
		}
	default:
		return data, fmt.Errorf("expected an array file or grouped file")
	}
	return data, nil
}

// asGrouped keeps array records without a check reason unverified. Checked
// records retain their reason and log when a workflow needs a grouped file.
func (f domainFile) asGrouped() ExtendedGroupedData {
	if !f.isArray {
		return f.grouped
	}
	var out ExtendedGroupedData
	for _, rec := range f.array {
		out.appendArrayRecord(rec)
	}
	return out
}

func (d *ExtendedGroupedData) appendArrayRecord(rec DomainRecord) {
	if !rec.Available && rec.Reason == "" {
		d.Unverified = append(d.Unverified, rec)
		return
	}
	grouped := ConvertArrayToGrouped([]DomainRecord{rec})
	d.Available = append(d.Available, grouped.Available...)
	d.Unavailable = append(d.Unavailable, grouped.Unavailable...)
}

// normalizeDomain rewrites a raw domain into label.com, or rejects it.
func normalizeDomain(domain string) string {
	d := strings.TrimSpace(strings.ToLower(domain))
	for strings.HasSuffix(d, ".com.com") {
		d = strings.TrimSuffix(d, ".com")
	}
	for strings.Contains(d, "..") {
		d = strings.ReplaceAll(d, "..", ".")
	}
	if !strings.HasSuffix(d, ".com") {
		return ""
	}
	label := strings.TrimSuffix(d, ".com")
	if len(label) > 63 || !validDomainLabel.MatchString(label) {
		return ""
	}
	return d
}

// domainSet owns normalized identity and first-write-wins accumulation. Bucket
// order is available, unavailable, unverified; fresh checks use mergeGrouped instead.
type domainSet struct {
	data    ExtendedGroupedData
	seen    map[string]bool
	removed []string
}

func (s *domainSet) include(raw string) string {
	domain := normalizeDomain(raw)
	if domain == "" {
		s.removed = append(s.removed, raw)
		return ""
	}
	if s.seen[domain] {
		return ""
	}
	if s.seen == nil {
		s.seen = make(map[string]bool)
	}
	s.seen[domain] = true
	return domain
}

func (s *domainSet) add(data ExtendedGroupedData) {
	for _, bucket := range []struct {
		source []GroupedDomain
		target *[]GroupedDomain
	}{{data.Available, &s.data.Available}, {data.Unavailable, &s.data.Unavailable}} {
		for _, rec := range bucket.source {
			if domain := s.include(rec.Domain); domain != "" {
				rec.Domain = domain
				*bucket.target = append(*bucket.target, rec)
			}
		}
	}
	for _, rec := range data.Unverified {
		if domain := s.include(rec.Domain); domain != "" {
			rec.Domain = domain
			s.data.Unverified = append(s.data.Unverified, rec)
		}
	}
}

func (s *domainSet) addFile(file domainFile) {
	if !file.isArray {
		s.add(file.grouped)
		return
	}
	// Deduplicate in array order before grouping, so a later available record
	// cannot displace an earlier unavailable or unverified occurrence.
	for _, rec := range file.array {
		if domain := s.include(rec.Domain); domain != "" {
			rec.Domain = domain
			s.data.appendArrayRecord(rec)
		}
	}
}

func writeSuggestionsFile(path string, list []DomainRecord) error {
	file, err := readDomainFile(path, true)
	if err != nil {
		return err
	}
	existing := file.asGrouped()
	var set domainSet
	set.add(existing)
	for _, rec := range list {
		if domain := set.include(rec.Domain); domain != "" {
			existing.Unverified = append(existing.Unverified, DomainRecord{Domain: domain})
		}
	}
	out, err := json.MarshalIndent(existing, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, out, 0644)
}

func cleanSuggestionsFile(path string) ([]string, error) {
	file, err := readDomainFile(path, false)
	if err != nil {
		return nil, err
	}
	var set domainSet
	var cleaned any
	if file.isArray {
		list := make([]DomainRecord, 0, len(file.array))
		for _, rec := range file.array {
			if domain := set.include(rec.Domain); domain != "" {
				rec.Domain = domain
				list = append(list, rec)
			}
		}
		cleaned = list
	} else {
		set.add(file.grouped)
		cleaned = set.data
	}
	out, err := json.MarshalIndent(cleaned, "", "  ")
	if err != nil {
		return set.removed, err
	}
	return set.removed, os.WriteFile(path, out, 0644)
}

// cleanTextFile preserves first occurrence order while cleaning a plain text list.
func cleanTextFile(path string) ([]string, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var set domainSet
	var domains []string
	for _, line := range strings.Split(string(raw), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if domain := set.include(line); domain != "" {
			domains = append(domains, domain)
		}
	}
	content := strings.Join(domains, "\n")
	if len(domains) > 0 {
		content += "\n"
	}
	return set.removed, os.WriteFile(path, []byte(content), 0644)
}

func mergeFiles(outputFile string, inputFiles []string) (int, error) {
	var set domainSet
	for _, path := range inputFiles {
		file, err := readDomainFile(path, false)
		if err != nil {
			return 0, err
		}
		set.addFile(file)
	}
	total := len(set.seen)
	out, err := json.MarshalIndent(set.data, "", "  ")
	if err != nil {
		return total, err
	}
	return total, os.WriteFile(outputFile, out, 0644)
}

func exportAvailableDomains(inputFile, outputFile string) (int, error) {
	file, err := readDomainFile(inputFile, false)
	if err != nil {
		return 0, err
	}
	var domains []string
	for _, rec := range file.asGrouped().Available {
		domains = append(domains, rec.Domain)
	}
	content := strings.Join(domains, "\n")
	if len(domains) > 0 {
		content += "\n"
	}
	if err := os.WriteFile(outputFile, []byte(content), 0644); err != nil {
		return 0, fmt.Errorf("writing %s: %w", outputFile, err)
	}
	return len(domains), nil
}

func (f domainFile) existingDomains() []string {
	var set domainSet
	set.add(f.asGrouped())
	data := set.data
	var domains []string
	for _, bucket := range [][]GroupedDomain{data.Available, data.Unavailable} {
		for _, rec := range bucket {
			domains = append(domains, rec.Domain)
		}
	}
	for _, rec := range data.Unverified {
		domains = append(domains, rec.Domain)
	}
	return domains
}
