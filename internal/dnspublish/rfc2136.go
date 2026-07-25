package dnspublish

import (
	"context"
	"errors"
	"fmt"
	"net"
	"strings"
	"time"

	"github.com/miekg/dns"
)

type RFC2136Config struct {
	Server    string
	Zone      string
	KeyName   string
	KeySecret string
	Algorithm string
}

type RFC2136Provider struct {
	server     string
	zone       string
	keyName    string
	algorithm  string
	client     *dns.Client
	exchange   func(context.Context, *dns.Msg, string) (*dns.Msg, time.Duration, error)
	transferIn func(*dns.Msg, string) (<-chan *dns.Envelope, error)
}

func NewRFC2136(config RFC2136Config) (*RFC2136Provider, error) {
	if _, _, err := net.SplitHostPort(config.Server); err != nil {
		return nil, errors.New("RFC 2136 server must include a host and port")
	}
	zone := dns.Fqdn(strings.ToLower(config.Zone))
	if zone == "." {
		return nil, errors.New("RFC 2136 zone is required")
	}
	if (config.KeyName == "") != (config.KeySecret == "") {
		return nil, errors.New("RFC 2136 key name and secret must be configured together")
	}
	algorithm := normalizeTSIGAlgorithm(config.Algorithm)
	if algorithm == "" {
		return nil, fmt.Errorf("unsupported TSIG algorithm %q", config.Algorithm)
	}
	keyName := ""
	secrets := map[string]string(nil)
	if config.KeyName != "" {
		keyName = dns.CanonicalName(config.KeyName)
		secrets = map[string]string{keyName: config.KeySecret}
	}
	client := &dns.Client{Net: "tcp", Timeout: 15 * time.Second, TsigSecret: secrets}
	transfer := &dns.Transfer{DialTimeout: 15 * time.Second, ReadTimeout: 15 * time.Second, WriteTimeout: 15 * time.Second, TsigSecret: secrets}
	provider := &RFC2136Provider{server: config.Server, zone: zone, keyName: keyName, algorithm: algorithm, client: client}
	provider.exchange = client.ExchangeContext
	provider.transferIn = func(message *dns.Msg, server string) (<-chan *dns.Envelope, error) {
		return transfer.In(message, server)
	}
	return provider, nil
}

func (p *RFC2136Provider) List(ctx context.Context, name string, recordType RecordType) ([]Record, error) {
	message := new(dns.Msg)
	message.SetQuestion(dns.Fqdn(name), dnsType(recordType))
	response, _, err := p.exchange(ctx, message, p.server)
	if err != nil {
		return nil, err
	}
	if response.Rcode != dns.RcodeSuccess && response.Rcode != dns.RcodeNameError {
		return nil, fmt.Errorf("RFC 2136 query returned %s", dns.RcodeToString[response.Rcode])
	}
	return recordsFromRRs(response.Answer, recordType), nil
}

func (p *RFC2136Provider) ListZone(ctx context.Context, recordType RecordType) ([]Record, error) {
	message := new(dns.Msg)
	message.SetAxfr(p.zone)
	p.sign(message)
	envelopes, err := p.transferIn(message, p.server)
	if err != nil {
		return nil, err
	}
	var records []Record
	for {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case envelope, ok := <-envelopes:
			if !ok {
				return records, nil
			}
			if envelope.Error != nil {
				return nil, envelope.Error
			}
			records = append(records, recordsFromRRs(envelope.RR, recordType)...)
		}
	}
}

func (p *RFC2136Provider) Create(ctx context.Context, record Record) (Record, error) {
	resource, err := resourceRecord(record)
	if err != nil {
		return Record{}, err
	}
	message := new(dns.Msg)
	message.SetUpdate(p.zone)
	message.Insert([]dns.RR{resource})
	if err := p.update(ctx, message); err != nil {
		return Record{}, err
	}
	record.ID = providerRecordID(record)
	return record, nil
}

func (p *RFC2136Provider) Update(ctx context.Context, record Record) error {
	old, err := parseProviderRecordID(record.ID)
	if err != nil {
		return err
	}
	old.TTL = record.TTL
	oldResource, err := resourceRecord(old)
	if err != nil {
		return err
	}
	newResource, err := resourceRecord(record)
	if err != nil {
		return err
	}
	message := new(dns.Msg)
	message.SetUpdate(p.zone)
	message.Remove([]dns.RR{oldResource})
	message.Insert([]dns.RR{newResource})
	return p.update(ctx, message)
}

func (p *RFC2136Provider) Delete(ctx context.Context, id string) error {
	record, err := parseProviderRecordID(id)
	if err != nil {
		return err
	}
	record.TTL = 0
	resource, err := resourceRecord(record)
	if err != nil {
		return err
	}
	message := new(dns.Msg)
	message.SetUpdate(p.zone)
	message.Remove([]dns.RR{resource})
	return p.update(ctx, message)
}

func (p *RFC2136Provider) update(ctx context.Context, message *dns.Msg) error {
	p.sign(message)
	response, _, err := p.exchange(ctx, message, p.server)
	if err != nil {
		return err
	}
	if response.Rcode != dns.RcodeSuccess {
		return fmt.Errorf("RFC 2136 update returned %s", dns.RcodeToString[response.Rcode])
	}
	return nil
}

func (p *RFC2136Provider) sign(message *dns.Msg) {
	if p.keyName != "" {
		message.SetTsig(p.keyName, p.algorithm, 300, time.Now().Unix())
	}
}

func recordsFromRRs(resources []dns.RR, recordType RecordType) []Record {
	var records []Record
	for _, resource := range resources {
		var content string
		switch value := resource.(type) {
		case *dns.AAAA:
			if recordType != RecordAAAA {
				continue
			}
			content = value.AAAA.String()
		case *dns.TXT:
			if recordType != RecordTXT {
				continue
			}
			content = strings.Join(value.Txt, "")
		default:
			continue
		}
		record := Record{Type: recordType, Name: strings.TrimSuffix(strings.ToLower(resource.Header().Name), "."), Content: content, TTL: int(resource.Header().Ttl)}
		record.ID = providerRecordID(record)
		records = append(records, record)
	}
	return records
}

func resourceRecord(record Record) (dns.RR, error) {
	header := dns.RR_Header{Name: dns.Fqdn(record.Name), Rrtype: dnsType(record.Type), Class: dns.ClassINET, Ttl: uint32(record.TTL)}
	switch record.Type {
	case RecordAAAA:
		address := net.ParseIP(record.Content)
		if address == nil || address.To4() != nil {
			return nil, errors.New("RFC 2136 AAAA content is invalid")
		}
		return &dns.AAAA{Hdr: header, AAAA: address}, nil
	case RecordTXT:
		return &dns.TXT{Hdr: header, Txt: []string{record.Content}}, nil
	default:
		return nil, fmt.Errorf("unsupported record type %s", record.Type)
	}
}

func dnsType(recordType RecordType) uint16 {
	if recordType == RecordTXT {
		return dns.TypeTXT
	}
	return dns.TypeAAAA
}

func normalizeTSIGAlgorithm(algorithm string) string {
	switch strings.ToLower(strings.TrimSpace(algorithm)) {
	case "", "hmac-sha256", "hmac-sha256.":
		return dns.HmacSHA256
	case "hmac-sha512", "hmac-sha512.":
		return dns.HmacSHA512
	case "hmac-sha384", "hmac-sha384.":
		return dns.HmacSHA384
	case "hmac-sha224", "hmac-sha224.":
		return dns.HmacSHA224
	default:
		return ""
	}
}
