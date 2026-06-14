package activesync

import (
	"time"

	"github.com/umailserver/umailserver/internal/activesync/wbxml"
	"github.com/umailserver/umailserver/internal/vacation"
)

// This file implements the EAS Settings command (MS-ASCMD 2.2.2.20, code page
// 18). A Settings request bundles one or more sub-commands — DeviceInformation,
// DevicePassword, UserInformation and Oof — each carrying a Get or Set. The
// response opens with a top-level Status and then mirrors one block per
// sub-command in request order, each with the shape the reference clients
// expect.

// Settings status codes (MS-ASCMD 2.2.3.x): 1 = success, 2 = protocol error.
const (
	settingsStatusSuccess  = "1"
	settingsStatusProtoErr = "2"
)

// handleSettings dispatches each sub-command in the request and assembles the
// response. Unknown sub-commands are skipped rather than guessed. The top-level
// Status reports the request was understood; per-sub-command Status reports each
// outcome.
func (s *Server) handleSettings(ctx *Context) ([]byte, error) {
	root, err := wbxml.Unmarshal(ctx.Body)
	if err != nil {
		return nil, err
	}

	resp := settingsEl("Settings", settingsStatus(settingsStatusSuccess))
	for _, sub := range root.Children {
		switch sub.Name {
		case "Oof":
			resp.Children = append(resp.Children, s.settingsOof(ctx, sub))
		case "DeviceInformation":
			resp.Children = append(resp.Children, s.settingsDeviceInformation(ctx, sub))
		case "DevicePassword":
			resp.Children = append(resp.Children, settingsDevicePassword())
		case "UserInformation":
			resp.Children = append(resp.Children, s.settingsUserInformation(ctx))
		}
	}
	return wbxml.Marshal(resp)
}

// settingsOof answers an Oof Get or applies an Oof Set. OOF state is the canonical
// out-of-office policy (the same one webmail/EWS/JMAP read and write through the
// vacation accessor), so a reply set from a phone fires at delivery and shows up
// on every other surface. A Get replies Oof>Status>Get>{...}; a Set replies the
// bare Oof>Status the protocol expects.
func (s *Server) settingsOof(ctx *Context, sub *wbxml.Element) *wbxml.Element {
	if set := sub.Sub("Set"); set != nil {
		return settingsEl("Oof", settingsStatus(s.applyOof(ctx.Email, set)))
	}
	return settingsEl("Oof", settingsStatus(settingsStatusSuccess), s.oofGetBody(ctx.Email))
}

// oofGetBody builds the Oof Get payload from the canonical OOF policy: the
// OofState, the schedule window when time-based, and one OofMessage per audience
// (internal, known external, unknown external). When no OOF store is wired or the
// policy is absent it reports a disabled state.
func (s *Server) oofGetBody(email string) *wbxml.Element {
	cfg := s.currentOOF(email)
	get := settingsEl("Get", settingsText("OofState", oofStateFromConfig(cfg)))
	if oofStateFromConfig(cfg) == oofStateScheduled {
		get.Children = append(get.Children,
			settingsText("StartTime", formatOofTime(cfg.StartDate)),
			settingsText("EndTime", formatOofTime(cfg.EndDate)),
		)
	}
	ext := externalReply(cfg)
	get.Children = append(get.Children,
		oofMessage("AppliesToInternal", cfg.Enabled, cfg.Message),
		oofMessage("AppliesToExternalKnown", cfg.Enabled && audienceIncludesKnown(cfg.Audience), ext),
		oofMessage("AppliesToExternalUnknown", cfg.Enabled && audienceIncludesUnknown(cfg.Audience), ext),
	)
	return get
}

// applyOof maps an Oof Set onto the canonical OOF policy and persists it through
// the shared vacation accessor (which also recompiles the user's Sieve so the
// auto-reply actually fires). It starts from the current policy so a disable does
// not erase the stored reply text. Returns the per-sub-command Status.
func (s *Server) applyOof(email string, set *wbxml.Element) string {
	if s.oof == nil {
		return settingsStatusSuccess // accepted; nothing to persist in this deployment
	}
	cfg := s.currentOOF(email)
	switch subText(set, "OofState") {
	case oofStateDisabled:
		cfg.Enabled = false
	case oofStateEnabled, oofStateScheduled:
		cfg.Enabled = true
		applyOofMessages(cfg, set)
		if subText(set, "OofState") == oofStateScheduled {
			cfg.StartDate = parseOofTime(subText(set, "StartTime"))
			cfg.EndDate = parseOofTime(subText(set, "EndTime"))
		} else {
			cfg.StartDate, cfg.EndDate = time.Time{}, time.Time{}
		}
		// EAS carries no OOF subject; the auto-reply runtime needs one, so default
		// it when the user has not set one through another surface.
		if cfg.Subject == "" {
			cfg.Subject = "Automatic reply"
		}
	default:
		return settingsStatusProtoErr
	}
	if err := s.oof.SetVacationConfig(email, cfg); err != nil {
		s.logger.Warn("activesync: set OOF failed", "email", email, "error", err)
		return settingsStatusProtoErr
	}
	return settingsStatusSuccess
}

// currentOOF returns the mailbox's current OOF policy as a vacation.Config, or an
// empty (disabled) config when none is wired or stored.
func (s *Server) currentOOF(email string) *vacation.Config {
	if s.oof != nil {
		if cfg, err := s.oof.GetVacationConfig(email); err == nil && cfg != nil {
			return cfg
		}
	}
	return &vacation.Config{}
}

// applyOofMessages folds the per-audience OofMessage blocks of a Set into the
// config: the internal block sets the internal reply, the external blocks set the
// external reply, and which external blocks are enabled selects the audience.
func applyOofMessages(cfg *vacation.Config, set *wbxml.Element) {
	var known, unknown bool
	for _, m := range set.Children {
		if m.Name != "OofMessage" {
			continue
		}
		reply := subText(m, "ReplyMessage")
		enabled := subText(m, "Enabled") == "1"
		switch {
		case m.Sub("AppliesToInternal") != nil:
			if reply != "" {
				cfg.Message = reply
			}
		case m.Sub("AppliesToExternalKnown") != nil:
			known = enabled
			if reply != "" {
				cfg.ExternalMessage = reply
			}
		case m.Sub("AppliesToExternalUnknown") != nil:
			unknown = enabled
			if reply != "" {
				cfg.ExternalMessage = reply
			}
		}
	}
	switch {
	case unknown:
		cfg.Audience = "all"
	case known:
		cfg.Audience = "external"
	default:
		cfg.Audience = "internal"
	}
}

// OOF state values exchanged in the Settings Oof OofState element (MS-ASCMD):
// 0 = disabled, 1 = enabled (global), 2 = time-based (scheduled).
const (
	oofStateDisabled  = "0"
	oofStateEnabled   = "1"
	oofStateScheduled = "2"
)

// oofStateFromConfig derives the EAS OofState from a vacation.Config. A disabled
// config reports state 0 regardless of any stored schedule window — mirroring the
// canonical guard so OOF set from a phone agrees with what webmail/EWS report.
func oofStateFromConfig(cfg *vacation.Config) string {
	if !cfg.Enabled {
		return oofStateDisabled
	}
	if !cfg.StartDate.IsZero() || !cfg.EndDate.IsZero() {
		return oofStateScheduled
	}
	return oofStateEnabled
}

// externalReply is the body sent to external senders, falling back to the
// internal reply when no distinct external reply is set.
func externalReply(cfg *vacation.Config) string {
	if cfg.ExternalMessage != "" {
		return cfg.ExternalMessage
	}
	return cfg.Message
}

// audienceIncludesKnown reports whether known external senders receive a reply.
// An empty audience defaults to everyone (matching vacation.Config semantics).
func audienceIncludesKnown(aud string) bool {
	return aud == "external" || aud == "all" || aud == ""
}

// audienceIncludesUnknown reports whether unknown external senders receive a reply.
func audienceIncludesUnknown(aud string) bool {
	return aud == "all" || aud == ""
}

// oofMessage builds an OofMessage block: the audience flag (an empty element),
// the enabled flag, the reply body, and its body type.
func oofMessage(appliesTo string, enabled bool, reply string) *wbxml.Element {
	en := "0"
	if enabled {
		en = "1"
	}
	return settingsEl("OofMessage",
		settingsEl(appliesTo),
		settingsText("Enabled", en),
		settingsText("ReplyMessage", reply),
		settingsText("BodyType", "Text"),
	)
}

// formatOofTime renders an OOF schedule bound in the EAS dashed ISO-8601 form,
// or "" for a zero time.
func formatOofTime(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.UTC().Format(easDateLayout)
}

// parseOofTime parses an OOF schedule bound, tolerating the millisecond and
// second-precision dashed ISO-8601 forms; an unparseable value yields a zero time.
func parseOofTime(s string) time.Time {
	if s == "" {
		return time.Time{}
	}
	if t, err := time.Parse(easDateLayout, s); err == nil {
		return t
	}
	if t, err := time.Parse("2006-01-02T15:04:05Z", s); err == nil {
		return t
	}
	return time.Time{}
}

// settingsDeviceInformation persists the device-identity attributes the client
// reports (Model, OS, IMEI, ...) onto the device partnership so they surface in
// the admin device list, then acknowledges with DeviceInformation>Status. The
// attributes are informational; persistence is best-effort and never blocks the
// acknowledgment the client needs to proceed.
func (s *Server) settingsDeviceInformation(ctx *Context, sub *wbxml.Element) *wbxml.Element {
	if set := sub.Sub("Set"); set != nil {
		s.persistDeviceInformation(ctx, set)
	}
	return settingsEl("DeviceInformation", settingsStatus(settingsStatusSuccess))
}

// persistDeviceInformation writes the reported attributes onto the existing
// partnership (the provisioning gate guarantees one exists). The client sends a
// full snapshot of what it knows, so the fields are overwritten wholesale.
func (s *Server) persistDeviceInformation(ctx *Context, set *wbxml.Element) {
	if s.devices == nil {
		return
	}
	deviceID := ctx.Request.URL.Query().Get("DeviceId")
	if deviceID == "" {
		return
	}
	dev, err := s.devices.GetEASDevice(ctx.Email, deviceID)
	if err != nil || dev == nil {
		return
	}
	dev.Model = subText(set, "Model")
	dev.IMEI = subText(set, "IMEI")
	dev.FriendlyName = subText(set, "FriendlyName")
	dev.OS = subText(set, "OS")
	dev.OSLanguage = subText(set, "OSLanguage")
	dev.PhoneNumber = subText(set, "PhoneNumber")
	dev.MobileOperator = subText(set, "MobileOperator")
	if err := s.devices.PutEASDevice(dev); err != nil {
		s.logger.Warn("activesync: persist device information failed",
			"email", ctx.Email, "device", deviceID, "error", err)
	}
}

// settingsDevicePassword acknowledges a DevicePassword Set or Clear. The
// provisioning policy is permissive — no device password is ever required — so
// there is nothing to store; the command is acknowledged so a compliant client
// proceeds. The response nests Status under Set (DevicePassword>Set>Status),
// matching the shape reference clients expect.
func settingsDevicePassword() *wbxml.Element {
	return settingsEl("DevicePassword", settingsEl("Set", settingsStatus(settingsStatusSuccess)))
}

// settingsUserInformation answers a UserInformation Get with the account's SMTP
// addresses. Since the server advertises 14.1+ the addresses live under
// Accounts>Account>EmailAddresses (MS-ASCMD); the pre-14.1 flat EmailAddresses
// shape is not emitted. The address set is the canonical alias table — the same
// source the admin alias API reads — so UserInformation converges with every
// other surface rather than maintaining a parallel list.
func (s *Server) settingsUserInformation(ctx *Context) *wbxml.Element {
	addrs := []string{ctx.Email}
	if s.aliases != nil {
		if extra, err := s.aliases.AliasesFor(ctx.Email); err == nil {
			addrs = append(addrs, extra...)
		} else {
			s.logger.Warn("activesync: list aliases failed", "email", ctx.Email, "error", err)
		}
	}
	addrs = dedupeAddresses(addrs)

	emails := make([]*wbxml.Element, 0, len(addrs)+1)
	for _, a := range addrs {
		emails = append(emails, settingsText("SMTPAddress", a))
	}
	emails = append(emails, settingsText("PrimarySmtpAddress", ctx.Email))

	account := settingsEl("Account",
		settingsText("AccountId", ctx.Email),
		settingsText("AccountName", ctx.Email),
		settingsText("UserDisplayName", ctx.Email),
		settingsText("SendDisabled", "0"),
		settingsEl("EmailAddresses", emails...),
	)
	get := settingsEl("Get", settingsEl("Accounts", account))
	return settingsEl("UserInformation", settingsStatus(settingsStatusSuccess), get)
}

// AliasSource lists the additional SMTP addresses (aliases) that deliver to a
// mailbox, used by the Settings UserInformation sub-command. It reads the
// canonical alias table so the address set matches every other surface.
type AliasSource interface {
	// AliasesFor returns the alias addresses delivering to email, excluding the
	// primary address itself.
	AliasesFor(email string) ([]string, error)
}

// OOFStore reads and writes the mailbox's out-of-office policy for the Settings
// Oof sub-command. It is the same canonical accessor webmail uses (the semcore
// policy store plus a Sieve recompile), so OOF set from a phone converges with
// webmail/EWS/JMAP and actually fires the auto-reply at delivery.
type OOFStore interface {
	GetVacationConfig(email string) (*vacation.Config, error)
	SetVacationConfig(email string, cfg *vacation.Config) error
}

// settingsEl builds a code-page-18 Settings element with the given children.
func settingsEl(name string, children ...*wbxml.Element) *wbxml.Element {
	return &wbxml.Element{Page: wbxml.PageSettings, Name: name, Children: children}
}

// settingsText builds a code-page-18 Settings leaf carrying text.
func settingsText(name, text string) *wbxml.Element {
	return &wbxml.Element{Page: wbxml.PageSettings, Name: name, Text: text}
}

// settingsStatus builds a Settings Status leaf.
func settingsStatus(code string) *wbxml.Element { return settingsText("Status", code) }

// subText returns the text of the first child named name, or "" when absent.
func subText(e *wbxml.Element, name string) string {
	if e == nil {
		return ""
	}
	if c := e.Sub(name); c != nil {
		return c.Text
	}
	return ""
}

// dedupeAddresses returns addrs with duplicates removed, preserving first-seen
// order so the primary stays first.
func dedupeAddresses(addrs []string) []string {
	seen := make(map[string]struct{}, len(addrs))
	out := make([]string, 0, len(addrs))
	for _, a := range addrs {
		if a == "" {
			continue
		}
		if _, ok := seen[a]; ok {
			continue
		}
		seen[a] = struct{}{}
		out = append(out, a)
	}
	return out
}
