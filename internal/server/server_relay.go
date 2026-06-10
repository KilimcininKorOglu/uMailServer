package server

import (
	"hash/fnv"
	"net"
	"strings"
)

// domainEgressIPGroupKey is the per-domain setting holding the name of the
// configured Relay IP group the domain's outbound mail egresses from.
const domainEgressIPGroupKey = "egress.ip_group"

// resolveEgressIP maps a sender domain to the source IP its outbound mail must
// bind, or "" for the default route. The domain names an IP group via its
// `egress.ip_group` setting; the group's IPs come from the live Relay config
// (so changes hot-reload). When a group lists several IPs, the domain is mapped
// to one of them stably (by hash) so it keeps a consistent source IP for
// sending reputation. Returns "" when no group is set, the group is unknown, or
// it lists no usable IPs.
func (s *Server) resolveEgressIP(senderDomain string) string {
	senderDomain = strings.ToLower(strings.TrimSpace(senderDomain))
	if senderDomain == "" || s.database == nil {
		return ""
	}
	dom, err := s.database.GetDomain(senderDomain)
	if err != nil || dom == nil {
		return ""
	}
	group := strings.TrimSpace(dom.Settings[domainEgressIPGroupKey])
	if group == "" {
		return ""
	}
	ips := s.egressGroupIPs(group)
	if len(ips) == 0 {
		return ""
	}
	// Stable per-domain pick: the same domain always selects the same IP within
	// its group, so its outbound reputation stays anchored to one address.
	h := fnv.New32a()
	_, _ = h.Write([]byte(senderDomain))
	return ips[h.Sum32()%uint32(len(ips))]
}

// egressGroupIPs returns the valid IP addresses configured for the named Relay
// IP group from the live config, skipping malformed entries.
func (s *Server) egressGroupIPs(group string) []string {
	var ips []string
	for _, g := range s.cfg().Relay.IPGroups {
		if !strings.EqualFold(strings.TrimSpace(g.Name), group) {
			continue
		}
		for _, ip := range g.IPs {
			ip = strings.TrimSpace(ip)
			if net.ParseIP(ip) != nil {
				ips = append(ips, ip)
			}
		}
		break
	}
	return ips
}
