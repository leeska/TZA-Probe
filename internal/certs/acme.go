package certs

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"encoding/pem"
	"errors"
	"fmt"
	"net"
	"slices"
	"strings"
	"time"

	"golang.org/x/crypto/acme"
)

// Let's Encrypt directories.
const (
	DirectoryProduction = "https://acme-v02.api.letsencrypt.org/directory"
	DirectoryStaging    = "https://acme-staging-v02.api.letsencrypt.org/directory"
)

// Issued is a certificate and its key as PEM, with what the panel shows.
type Issued struct {
	CertPEM, KeyPEM     string
	Serial, Issuer      string
	NotBefore, NotAfter time.Time
}

// DNSProvider places and removes DNS-01 records.
type DNSProvider interface {
	Zone(ctx context.Context, name string) (Zone, error)
	AddTXT(ctx context.Context, zoneID, name, content string) (string, error)
	DeleteRecord(ctx context.Context, zoneID, id string) error
}

// Issuer gets a certificate for domains.
type Issuer interface {
	Issue(ctx context.Context, domains []string, dns DNSProvider, log func(string)) (Issued, error)
}

// ACME issues from an ACME CA (Let's Encrypt) with DNS-01. The account key
// is made on first use and kept in certs_state, one per directory.
type ACME struct {
	Store     *Store
	Directory string
	// LookupTXT asks a name server (host:port) for TXT records; nil uses DNS
	// over UDP. Tests replace it.
	LookupTXT func(ctx context.Context, server, name string) ([]string, error)
	// Propagation bounds the wait for the records to show (default 3 min).
	Propagation time.Duration
}

func (a *ACME) client(ctx context.Context) (*acme.Client, error) {
	key := "account_key:" + a.Directory
	raw, err := a.Store.State(ctx, key)
	if err != nil {
		return nil, err
	}
	var signer *ecdsa.PrivateKey
	if raw != "" {
		block, _ := pem.Decode([]byte(raw))
		if block == nil {
			return nil, errors.New("ACME 账号密钥损坏")
		}
		if signer, err = x509.ParseECPrivateKey(block.Bytes); err != nil {
			return nil, err
		}
	} else {
		if signer, err = ecdsa.GenerateKey(elliptic.P256(), rand.Reader); err != nil {
			return nil, err
		}
		der, _ := x509.MarshalECPrivateKey(signer)
		if err := a.Store.SetState(ctx, key, string(pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: der}))); err != nil {
			return nil, err
		}
	}
	c := &acme.Client{Key: signer, DirectoryURL: a.Directory, UserAgent: "tza-probe"}
	if _, err := c.Register(ctx, &acme.Account{}, acme.AcceptTOS); err != nil && !errors.Is(err, acme.ErrAccountAlreadyExists) {
		return nil, fmt.Errorf("注册 ACME 账号失败：%w", err)
	}
	return c, nil
}

func (a *ACME) lookup(ctx context.Context, server, name string) ([]string, error) {
	if a.LookupTXT != nil {
		return a.LookupTXT(ctx, server, name)
	}
	r := &net.Resolver{PreferGo: true, Dial: func(ctx context.Context, network, _ string) (net.Conn, error) {
		return (&net.Dialer{Timeout: 5 * time.Second}).DialContext(ctx, network, server)
	}}
	return r.LookupTXT(ctx, name)
}

type placed struct {
	zone   Zone
	record string
	name   string
	value  string
	chal   *acme.Challenge
	authz  string
}

func (a *ACME) Issue(ctx context.Context, domains []string, dns DNSProvider, log func(string)) (Issued, error) {
	ctx, cancel := context.WithTimeout(ctx, 15*time.Minute)
	defer cancel()
	c, err := a.client(ctx)
	if err != nil {
		return Issued{}, err
	}
	order, err := c.AuthorizeOrder(ctx, acme.DomainIDs(domains...))
	if err != nil {
		return Issued{}, fmt.Errorf("创建订单失败：%w", err)
	}
	var records []placed
	defer func() {
		cleanup, cancel := context.WithTimeout(context.Background(), time.Minute)
		defer cancel()
		for _, p := range records {
			if err := dns.DeleteRecord(cleanup, p.zone.ID, p.record); err != nil {
				log("删除验证记录 " + p.name + " 失败：" + err.Error())
			}
		}
	}()
	for _, u := range order.AuthzURLs {
		z, err := c.GetAuthorization(ctx, u)
		if err != nil {
			return Issued{}, err
		}
		if z.Status == acme.StatusValid {
			continue
		}
		var chal *acme.Challenge
		for _, ch := range z.Challenges {
			if ch.Type == "dns-01" {
				chal = ch
			}
		}
		if chal == nil {
			return Issued{}, fmt.Errorf("%s 没有 DNS 验证方式", z.Identifier.Value)
		}
		value, err := c.DNS01ChallengeRecord(chal.Token)
		if err != nil {
			return Issued{}, err
		}
		name := "_acme-challenge." + z.Identifier.Value
		zone, err := dns.Zone(ctx, name)
		if err != nil {
			return Issued{}, err
		}
		id, err := dns.AddTXT(ctx, zone.ID, name, value)
		if err != nil {
			return Issued{}, fmt.Errorf("添加 %s 的 TXT 记录失败：%w", name, err)
		}
		records = append(records, placed{zone: zone, record: id, name: name, value: value, chal: chal, authz: z.URI})
		log("已添加验证记录 " + name)
	}
	for _, p := range records {
		if err := a.propagated(ctx, p); err != nil {
			return Issued{}, err
		}
	}
	for _, p := range records {
		if _, err := c.Accept(ctx, p.chal); err != nil {
			return Issued{}, fmt.Errorf("提交验证失败：%w", err)
		}
	}
	for _, p := range records {
		if _, err := c.WaitAuthorization(ctx, p.authz); err != nil {
			return Issued{}, fmt.Errorf("%s 验证没通过：%w", strings.TrimPrefix(p.name, "_acme-challenge."), err)
		}
	}
	log("域名验证通过")
	if order, err = c.WaitOrder(ctx, order.URI); err != nil {
		return Issued{}, err
	}
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return Issued{}, err
	}
	csr, err := x509.CreateCertificateRequest(rand.Reader, &x509.CertificateRequest{Subject: pkix.Name{CommonName: strings.TrimPrefix(domains[0], "*.")}, DNSNames: domains}, key)
	if err != nil {
		return Issued{}, err
	}
	chain, _, err := c.CreateOrderCert(ctx, order.FinalizeURL, csr, true)
	if err != nil {
		return Issued{}, fmt.Errorf("签发失败：%w", err)
	}
	if len(chain) == 0 {
		return Issued{}, errors.New("CA 没有返回证书")
	}
	leaf, err := x509.ParseCertificate(chain[0])
	if err != nil {
		return Issued{}, err
	}
	var certPEM strings.Builder
	for _, der := range chain {
		pem.Encode(&certPEM, &pem.Block{Type: "CERTIFICATE", Bytes: der})
	}
	keyDER, _ := x509.MarshalECPrivateKey(key)
	return Issued{
		CertPEM: certPEM.String(), KeyPEM: string(pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER})),
		Serial: hex.EncodeToString(leaf.SerialNumber.Bytes()), Issuer: leaf.Issuer.CommonName,
		NotBefore: leaf.NotBefore, NotAfter: leaf.NotAfter,
	}, nil
}

// propagated waits until every name server of the zone answers the record.
func (a *ACME) propagated(ctx context.Context, p placed) error {
	limit := a.Propagation
	if limit == 0 {
		limit = 3 * time.Minute
	}
	deadline := time.Now().Add(limit)
	servers := p.zone.NameServers
	if len(servers) == 0 {
		servers = []string{"1.1.1.1"}
	}
	for {
		ok := true
		for _, ns := range servers {
			values, err := a.lookup(ctx, net.JoinHostPort(ns, "53"), p.name)
			if err != nil || !slices.Contains(values, p.value) {
				ok = false
				break
			}
		}
		if ok {
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("等了 %s，%s 的 TXT 记录还没在 DNS 上生效", limit, p.name)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(5 * time.Second):
		}
	}
}
