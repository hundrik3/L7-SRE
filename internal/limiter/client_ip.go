package limiter

import (
	"net"
	"net/http"
	"net/netip"
	"strings"
)

// IPResolver trusts forwarding headers only from explicitly trusted proxies.
type IPResolver struct{ trusted []netip.Prefix }

func NewIPResolver(cidrs []string) (*IPResolver, error) {
	r := &IPResolver{}
	for _, cidr := range cidrs {
		p, err := netip.ParsePrefix(cidr)
		if err != nil {
			return nil, err
		}
		r.trusted = append(r.trusted, p.Masked())
	}
	return r, nil
}

func (r *IPResolver) isTrusted(addr netip.Addr) bool {
	for _, prefix := range r.trusted {
		if prefix.Contains(addr) {
			return true
		}
	}
	return false
}

func (r *IPResolver) ClientIP(req *http.Request) string {
	host, _, err := net.SplitHostPort(req.RemoteAddr)
	if err != nil {
		host = req.RemoteAddr
	}
	peer, err := netip.ParseAddr(host)
	if err != nil {
		return "unknown"
	}
	peer = peer.Unmap()
	if !r.isTrusted(peer) {
		return peer.String()
	}
	value := strings.Join(req.Header.Values("X-Forwarded-For"), ",")
	if value == "" || len(value) > 4096 {
		return peer.String()
	}
	parts := strings.Split(value, ",")
	chain := make([]netip.Addr, len(parts))
	for i, part := range parts {
		addr, err := netip.ParseAddr(strings.TrimSpace(part))
		if err != nil {
			return peer.String()
		}
		chain[i] = addr.Unmap()
	}
	client := peer
	for i := len(chain) - 1; i >= 0 && r.isTrusted(client); i-- {
		client = chain[i]
	}
	return client.String()
}
