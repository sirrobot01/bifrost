package dnspublish

import "context"

// RecordType identifies a supported DNS record type.
type RecordType string

const (
	RecordAAAA RecordType = "AAAA"
	RecordTXT  RecordType = "TXT"
)

// Record is the provider-neutral form of a managed DNS record.
type Record struct {
	ID      string
	Type    RecordType
	Name    string
	Content string
	TTL     int
	Proxied bool
}

// Provider performs primitive DNS record operations.
type Provider interface {
	List(context.Context, string, RecordType) ([]Record, error)
	ListZone(context.Context, RecordType) ([]Record, error)
	Create(context.Context, Record) (Record, error)
	Update(context.Context, Record) error
	Delete(context.Context, string) error
}
