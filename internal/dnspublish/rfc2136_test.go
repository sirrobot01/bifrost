package dnspublish

import (
	"context"
	"net"
	"testing"
	"time"

	"github.com/miekg/dns"
)

func TestRFC2136ProviderQueriesAndUpdates(t *testing.T) {
	t.Parallel()

	provider, err := NewRFC2136(RFC2136Config{Server: "127.0.0.1:53", Zone: "example.com"})
	if err != nil {
		t.Fatal(err)
	}
	var update *dns.Msg
	provider.exchange = func(_ context.Context, message *dns.Msg, _ string) (*dns.Msg, time.Duration, error) {
		response := new(dns.Msg)
		response.SetReply(message)
		if message.Opcode == dns.OpcodeQuery {
			response.Answer = []dns.RR{&dns.AAAA{
				Hdr:  dns.RR_Header{Name: "media.example.com.", Rrtype: dns.TypeAAAA, Class: dns.ClassINET, Ttl: 60},
				AAAA: net.ParseIP("2001:db8::1"),
			}}
		} else {
			update = message.Copy()
		}
		return response, 0, nil
	}

	records, err := provider.List(t.Context(), "media.example.com", RecordAAAA)
	if err != nil {
		t.Fatal(err)
	}
	if len(records) != 1 || records[0].Content != "2001:db8::1" {
		t.Fatalf("records = %+v", records)
	}
	if _, err := provider.Create(t.Context(), Record{Name: "media.example.com", Type: RecordAAAA, Content: "2001:db8::2", TTL: 60}); err != nil {
		t.Fatal(err)
	}
	if update == nil || update.Opcode != dns.OpcodeUpdate || len(update.Ns) != 1 {
		t.Fatalf("update = %+v", update)
	}
}

func TestRFC2136ProviderSignsUpdates(t *testing.T) {
	t.Parallel()

	provider, err := NewRFC2136(RFC2136Config{Server: "127.0.0.1:53", Zone: "example.com", KeyName: "bifrost-key", KeySecret: "c2VjcmV0", Algorithm: "hmac-sha256"})
	if err != nil {
		t.Fatal(err)
	}
	provider.exchange = func(_ context.Context, message *dns.Msg, _ string) (*dns.Msg, time.Duration, error) {
		if message.IsTsig() == nil {
			t.Fatal("update was not TSIG-signed")
		}
		response := new(dns.Msg)
		response.SetReply(message)
		return response, 0, nil
	}
	if _, err := provider.Create(t.Context(), Record{Name: "_bifrost.media.example.com", Type: RecordTXT, Content: "bifrost-owner=home", TTL: 60}); err != nil {
		t.Fatal(err)
	}
}
