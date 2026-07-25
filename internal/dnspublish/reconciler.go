package dnspublish

import (
	"context"
	"errors"
	"fmt"
	"net/netip"
	"slices"
	"strings"
	"time"
)

const ownershipPrefix = "bifrost-owner="

// Publication is the complete desired DNS state for one service.
type Publication struct {
	Name          string
	Addresses     []netip.Addr
	EdgeAddresses []netip.Addr
	TTL           time.Duration
}

// Reconciler safely owns and updates DNS publications.
type Reconciler struct {
	provider Provider
	ownerID  string
}

// NewReconciler returns a DNS reconciler for ownerID.
func NewReconciler(provider Provider, ownerID string) (*Reconciler, error) {
	if provider == nil {
		return nil, errors.New("DNS provider is required")
	}
	if ownerID == "" {
		return nil, errors.New("DNS owner ID is required")
	}
	for _, character := range ownerID {
		if character != '-' && character != '_' && character != '.' && (character < 'a' || character > 'z') && (character < 'A' || character > 'Z') && (character < '0' || character > '9') {
			return nil, errors.New("DNS owner ID may contain only letters, digits, dots, underscores, and hyphens")
		}
	}
	return &Reconciler{provider: provider, ownerID: ownerID}, nil
}

// Ensure reconciles owned DNS-only address records.
func (r *Reconciler) Ensure(ctx context.Context, publication Publication) error {
	publication, err := normalizePublication(publication)
	if err != nil {
		return err
	}

	markerName := ownershipName(publication.Name)
	markerContent := ownershipPrefix + r.ownerID
	markers, err := r.provider.List(ctx, markerName, RecordTXT)
	if err != nil {
		return fmt.Errorf("list DNS ownership records: %w", err)
	}
	ipv6Records, err := r.provider.List(ctx, publication.Name, RecordAAAA)
	if err != nil {
		return fmt.Errorf("list AAAA records: %w", err)
	}
	ipv4Records, err := r.provider.List(ctx, publication.Name, RecordA)
	if err != nil {
		return fmt.Errorf("list A records: %w", err)
	}

	if err := verifyOwnership(markers, markerContent, len(ipv6Records)+len(ipv4Records) > 0); err != nil {
		return fmt.Errorf("publish %s: %w", publication.Name, err)
	}
	if len(markers) == 0 {
		if _, err := r.provider.Create(ctx, Record{
			Type:    RecordTXT,
			Name:    markerName,
			Content: markerContent,
			TTL:     int(publication.TTL.Seconds()),
		}); err != nil {
			return fmt.Errorf("create DNS ownership record: %w", err)
		}
	}

	ttl := int(publication.TTL.Seconds())
	if err := r.reconcileRecords(ctx, publication.Name, RecordAAAA, publication.Addresses, ttl, ipv6Records); err != nil {
		return err
	}
	return r.reconcileRecords(ctx, publication.Name, RecordA, publication.EdgeAddresses, ttl, ipv4Records)
}

func (r *Reconciler) reconcileRecords(ctx context.Context, name string, recordType RecordType, addresses []netip.Addr, ttl int, records []Record) error {
	desired := make(map[string]struct{}, len(addresses))
	existing := make(map[string][]Record, len(records))
	for _, address := range addresses {
		desired[address.String()] = struct{}{}
	}
	for _, record := range records {
		existing[record.Content] = append(existing[record.Content], record)
	}
	for _, address := range addresses {
		content := address.String()
		matching := existing[content]
		if len(matching) == 0 {
			if _, err := r.provider.Create(ctx, Record{
				Type:    recordType,
				Name:    name,
				Content: content,
				TTL:     ttl,
				Proxied: false,
			}); err != nil {
				return fmt.Errorf("create %s record %s: %w", recordType, content, err)
			}
			continue
		}

		primary := matching[0]
		if (primary.TTL != 0 && primary.TTL != ttl) || primary.Proxied {
			primary.TTL = ttl
			primary.Proxied = false
			if err := r.provider.Update(ctx, primary); err != nil {
				return fmt.Errorf("update %s record %s: %w", recordType, content, err)
			}
		}
		for _, duplicate := range matching[1:] {
			if err := r.provider.Delete(ctx, duplicate.ID); err != nil {
				return fmt.Errorf("delete duplicate %s record %s: %w", recordType, duplicate.ID, err)
			}
		}
	}

	for _, record := range records {
		if _, keep := desired[record.Content]; keep {
			continue
		}
		if err := r.provider.Delete(ctx, record.ID); err != nil {
			return fmt.Errorf("delete stale %s record %s: %w", recordType, record.ID, err)
		}
	}

	return nil
}

func (r *Reconciler) Prune(ctx context.Context, desiredNames []string) error {
	desired := make(map[string]struct{}, len(desiredNames))
	for _, name := range desiredNames {
		normalized, err := normalizeName(name)
		if err != nil {
			return err
		}
		desired[normalized] = struct{}{}
	}
	markers, err := r.provider.ListZone(ctx, RecordTXT)
	if err != nil {
		return fmt.Errorf("list DNS ownership records: %w", err)
	}
	ownedContent := ownershipPrefix + r.ownerID
	pruned := make(map[string]struct{})
	for _, marker := range markers {
		if strings.Trim(marker.Content, "\"") != ownedContent || !strings.HasPrefix(marker.Name, "_bifrost.") {
			continue
		}
		name := strings.TrimPrefix(marker.Name, "_bifrost.")
		if _, keep := desired[name]; keep {
			continue
		}
		if _, alreadyPruned := pruned[name]; alreadyPruned {
			continue
		}
		if err := r.Withdraw(ctx, name); err != nil {
			return fmt.Errorf("prune DNS publication %s: %w", name, err)
		}
		pruned[name] = struct{}{}
	}
	return nil
}

// Withdraw removes owned address records before their ownership marker.
func (r *Reconciler) Withdraw(ctx context.Context, name string) error {
	name, err := normalizeName(name)
	if err != nil {
		return err
	}

	markerName := ownershipName(name)
	markerContent := ownershipPrefix + r.ownerID
	markers, err := r.provider.List(ctx, markerName, RecordTXT)
	if err != nil {
		return fmt.Errorf("list DNS ownership records: %w", err)
	}
	if len(markers) == 0 {
		return nil
	}
	if err := verifyOwnership(markers, markerContent, false); err != nil {
		return fmt.Errorf("withdraw %s: %w", name, err)
	}

	for _, recordType := range []RecordType{RecordAAAA, RecordA} {
		records, err := r.provider.List(ctx, name, recordType)
		if err != nil {
			return fmt.Errorf("list %s records: %w", recordType, err)
		}
		for _, record := range records {
			if err := r.provider.Delete(ctx, record.ID); err != nil {
				return fmt.Errorf("delete %s record %s: %w", recordType, record.ID, err)
			}
		}
	}
	for _, marker := range markers {
		if err := r.provider.Delete(ctx, marker.ID); err != nil {
			return fmt.Errorf("delete DNS ownership record %s: %w", marker.ID, err)
		}
	}
	return nil
}

func normalizePublication(publication Publication) (Publication, error) {
	name, err := normalizeName(publication.Name)
	if err != nil {
		return Publication{}, err
	}
	if publication.TTL < 60*time.Second || publication.TTL > 24*time.Hour || publication.TTL%time.Second != 0 {
		return Publication{}, errors.New("DNS TTL must be whole seconds between 60 seconds and 24 hours")
	}

	addresses := make(map[netip.Addr]struct{}, len(publication.Addresses))
	for _, address := range publication.Addresses {
		if !address.IsValid() || !address.Is6() || address.Is4In6() || !address.IsGlobalUnicast() || address.IsPrivate() {
			return Publication{}, fmt.Errorf("DNS address %s is not a public IPv6 address", address)
		}
		addresses[address] = struct{}{}
	}
	if len(addresses) == 0 {
		return Publication{}, errors.New("at least one DNS address is required")
	}

	publication.Name = name
	publication.Addresses = make([]netip.Addr, 0, len(addresses))
	for address := range addresses {
		publication.Addresses = append(publication.Addresses, address)
	}
	slices.SortFunc(publication.Addresses, netip.Addr.Compare)
	edgeAddresses := make(map[netip.Addr]struct{}, len(publication.EdgeAddresses))
	for _, address := range publication.EdgeAddresses {
		if !address.IsValid() || !address.Is4() || !address.IsGlobalUnicast() || address.IsPrivate() {
			return Publication{}, fmt.Errorf("edge DNS address %s is not a public IPv4 address", address)
		}
		edgeAddresses[address] = struct{}{}
	}
	publication.EdgeAddresses = make([]netip.Addr, 0, len(edgeAddresses))
	for address := range edgeAddresses {
		publication.EdgeAddresses = append(publication.EdgeAddresses, address)
	}
	slices.SortFunc(publication.EdgeAddresses, netip.Addr.Compare)
	return publication, nil
}

func normalizeName(name string) (string, error) {
	name = strings.ToLower(strings.TrimSuffix(strings.TrimSpace(name), "."))
	if name == "" || len(name) > 253-len("_bifrost.") {
		return "", errors.New("DNS name is empty or too long")
	}
	for _, label := range strings.Split(name, ".") {
		if label == "" || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return "", fmt.Errorf("invalid DNS name %q", name)
		}
		for _, character := range label {
			if character != '-' && (character < 'a' || character > 'z') && (character < '0' || character > '9') {
				return "", fmt.Errorf("invalid DNS name %q", name)
			}
		}
	}
	return name, nil
}

func ownershipName(name string) string {
	return "_bifrost." + name
}

func verifyOwnership(markers []Record, expected string, recordsExist bool) error {
	if len(markers) == 0 {
		if recordsExist {
			return errors.New("existing address records have no Bifrost ownership marker")
		}
		return nil
	}
	for _, marker := range markers {
		if strings.Trim(marker.Content, "\"") != expected {
			return fmt.Errorf("DNS name is owned by %q", marker.Content)
		}
	}
	return nil
}
