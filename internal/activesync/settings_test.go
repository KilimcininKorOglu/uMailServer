package activesync

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"slices"
	"testing"
	"time"

	"github.com/umailserver/umailserver/internal/activesync/wbxml"
	"github.com/umailserver/umailserver/internal/db"
	"github.com/umailserver/umailserver/internal/vacation"
)

// stubAliasSource is a deterministic AliasSource for UserInformation tests.
type stubAliasSource struct{ aliases []string }

func (s stubAliasSource) AliasesFor(string) ([]string, error) { return s.aliases, nil }

// stubOOFStore is an in-memory OOFStore that records the last config set, so OOF
// round-trip tests can assert what the handler persisted and what it reads back.
type stubOOFStore struct{ cfg *vacation.Config }

func (s *stubOOFStore) GetVacationConfig(string) (*vacation.Config, error) {
	if s.cfg == nil {
		return &vacation.Config{}, nil
	}
	return s.cfg, nil
}

func (s *stubOOFStore) SetVacationConfig(_ string, cfg *vacation.Config) error {
	s.cfg = cfg
	return nil
}

// settingsServer builds an EAS server with a seeded, provisioned device so a
// Settings command clears the provisioning gate (every command but Provision
// requires the device's current policy key).
func settingsServer(t *testing.T) (*Server, *db.DB) {
	t.Helper()
	s, database := provisionServer(t)
	if err := database.PutEASDevice(&db.EASDevice{
		Email: "bob@x.test", DeviceID: "DEV1", PolicyKey: "12345",
	}); err != nil {
		t.Fatalf("seed device: %v", err)
	}
	return s, database
}

// doSettings POSTs a Settings request through the full transport (clearing the
// provisioning gate with the seeded key) and returns the decoded response.
func doSettings(t *testing.T, s *Server, body *wbxml.Element) *wbxml.Element {
	t.Helper()
	b, err := wbxml.Marshal(body)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost,
		"/Microsoft-Server-ActiveSync?Cmd=Settings&DeviceId=DEV1", bytes.NewReader(b))
	req.Header.Set("X-MS-PolicyKey", "12345")
	s.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("Settings status = %d, want 200", rec.Code)
	}
	resp, err := wbxml.Unmarshal(rec.Body.Bytes())
	if err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	return resp
}

// smtpAddresses collects the SMTPAddress child texts of an EmailAddresses element.
func smtpAddresses(emails *wbxml.Element) []string {
	var out []string
	for _, c := range emails.Children {
		if c.Name == "SMTPAddress" {
			out = append(out, c.Text)
		}
	}
	return out
}

// TestSettingsDeviceInformationPersists proves a DeviceInformation Set both
// acknowledges (DeviceInformation>Status 1) and durably records the reported
// attributes on the partnership — the latter is what makes them visible to an
// admin. A handler that only acked would pass a Status-only check while silently
// dropping the data, so the persisted fields are asserted against the store.
func TestSettingsDeviceInformationPersists(t *testing.T) {
	s, database := settingsServer(t)

	body := settingsEl("Settings", settingsEl("DeviceInformation", settingsEl("Set",
		settingsText("Model", "iPhone15,2"),
		settingsText("IMEI", "490154203237518"),
		settingsText("FriendlyName", "Bob's iPhone"),
		settingsText("OS", "iOS 17.4"),
		settingsText("OSLanguage", "en-US"),
		settingsText("PhoneNumber", "+15551234567"),
		settingsText("MobileOperator", "Example Mobile"),
	)))
	resp := doSettings(t, s, body)

	if got := subText(resp, "Status"); got != settingsStatusSuccess {
		t.Fatalf("top Status = %q, want %q", got, settingsStatusSuccess)
	}
	di := resp.Sub("DeviceInformation")
	if di == nil || subText(di, "Status") != settingsStatusSuccess {
		t.Fatalf("DeviceInformation Status = %v, want 1", di)
	}

	dev, err := database.GetEASDevice("bob@x.test", "DEV1")
	if err != nil {
		t.Fatalf("GetEASDevice: %v", err)
	}
	for _, tc := range []struct{ name, got, want string }{
		{"Model", dev.Model, "iPhone15,2"},
		{"IMEI", dev.IMEI, "490154203237518"},
		{"FriendlyName", dev.FriendlyName, "Bob's iPhone"},
		{"OS", dev.OS, "iOS 17.4"},
		{"OSLanguage", dev.OSLanguage, "en-US"},
		{"PhoneNumber", dev.PhoneNumber, "+15551234567"},
		{"MobileOperator", dev.MobileOperator, "Example Mobile"},
	} {
		if tc.got != tc.want {
			t.Errorf("persisted %s = %q, want %q", tc.name, tc.got, tc.want)
		}
	}
}

// TestSettingsDevicePassword proves a DevicePassword Set is acknowledged with the
// nested DevicePassword>Set>Status shape clients parse. The permissive policy
// stores no password; the test guards the wire shape, not storage.
func TestSettingsDevicePassword(t *testing.T) {
	s, _ := settingsServer(t)

	body := settingsEl("Settings", settingsEl("DevicePassword", settingsEl("Set",
		settingsText("Password", "1234"),
	)))
	resp := doSettings(t, s, body)

	dp := resp.Sub("DevicePassword")
	if dp == nil {
		t.Fatal("response missing DevicePassword")
	}
	set := dp.Sub("Set")
	if set == nil || subText(set, "Status") != settingsStatusSuccess {
		t.Fatalf("DevicePassword>Set>Status = %v, want 1", set)
	}
}

// TestSettingsUserInformationReturnsAliases proves UserInformation Get returns
// the account's canonical address set — primary plus every active alias — in the
// 16.1 Accounts>Account>EmailAddresses shape. A regression that dropped aliases
// or emitted the pre-14.1 flat shape would fail here.
func TestSettingsUserInformationReturnsAliases(t *testing.T) {
	s, _ := settingsServer(t)
	s.SetAliasSource(stubAliasSource{aliases: []string{"bob.alias@x.test", "sales@x.test"}})

	body := settingsEl("Settings", settingsEl("UserInformation", settingsEl("Get")))
	resp := doSettings(t, s, body)

	ui := resp.Sub("UserInformation")
	if ui == nil || subText(ui, "Status") != settingsStatusSuccess {
		t.Fatalf("UserInformation Status = %v, want 1", ui)
	}
	get := ui.Sub("Get")
	if get == nil {
		t.Fatal("UserInformation missing Get")
	}
	accounts := get.Sub("Accounts")
	if accounts == nil {
		t.Fatal("UserInformation Get missing Accounts (16.1 shape)")
	}
	account := accounts.Sub("Account")
	if account == nil {
		t.Fatal("Accounts missing Account")
	}
	emails := account.Sub("EmailAddresses")
	if emails == nil {
		t.Fatal("Account missing EmailAddresses")
	}
	if got := subText(emails, "PrimarySmtpAddress"); got != "bob@x.test" {
		t.Errorf("PrimarySmtpAddress = %q, want bob@x.test", got)
	}
	addrs := smtpAddresses(emails)
	for _, want := range []string{"bob@x.test", "bob.alias@x.test", "sales@x.test"} {
		if !slices.Contains(addrs, want) {
			t.Errorf("SMTPAddress %q missing from %v", want, addrs)
		}
	}
}

// TestSettingsOofSetThenGet proves an Oof Set persists the canonical OOF policy
// and a following Oof Get reports it back: an enabled (global) reply with a
// distinct internal and external body and an everyone audience. This is the
// in-protocol half of the convergence; the cross-surface half (the same policy
// visible over the webmail vacation API) is exercised by the probe.
func TestSettingsOofSetThenGet(t *testing.T) {
	s, _ := settingsServer(t)
	store := &stubOOFStore{}
	s.SetOOFStore(store)

	set := settingsEl("Settings", settingsEl("Oof", settingsEl("Set",
		settingsText("OofState", oofStateEnabled),
		settingsEl("OofMessage",
			settingsEl("AppliesToInternal"),
			settingsText("Enabled", "1"),
			settingsText("ReplyMessage", "Out until Monday (internal)"),
			settingsText("BodyType", "Text"),
		),
		settingsEl("OofMessage",
			settingsEl("AppliesToExternalUnknown"),
			settingsText("Enabled", "1"),
			settingsText("ReplyMessage", "Away (external)"),
			settingsText("BodyType", "Text"),
		),
	)))
	resp := doSettings(t, s, set)
	if oof := resp.Sub("Oof"); oof == nil || subText(oof, "Status") != settingsStatusSuccess {
		t.Fatalf("Oof Set Status = %v, want 1", resp.Sub("Oof"))
	}

	// What was persisted must be a real enabled policy converging on the canonical
	// store — not just an acked no-op.
	if store.cfg == nil || !store.cfg.Enabled {
		t.Fatalf("Oof Set did not persist an enabled policy: %+v", store.cfg)
	}
	if store.cfg.Message != "Out until Monday (internal)" {
		t.Errorf("internal reply = %q", store.cfg.Message)
	}
	if store.cfg.ExternalMessage != "Away (external)" {
		t.Errorf("external reply = %q", store.cfg.ExternalMessage)
	}
	if store.cfg.Audience != "all" {
		t.Errorf("audience = %q, want all (external-unknown enabled)", store.cfg.Audience)
	}

	get := doSettings(t, s, settingsEl("Settings", settingsEl("Oof", settingsEl("Get"))))
	body := get.Sub("Oof").Sub("Get")
	if body == nil {
		t.Fatal("Oof Get missing Get body")
	}
	if st := subText(body, "OofState"); st != oofStateEnabled {
		t.Fatalf("OofState = %q, want %q", st, oofStateEnabled)
	}
	internalOn, externalUnknownOn := false, false
	for _, m := range body.Children {
		if m.Name != "OofMessage" {
			continue
		}
		switch {
		case m.Sub("AppliesToInternal") != nil:
			internalOn = subText(m, "Enabled") == "1" && subText(m, "ReplyMessage") == "Out until Monday (internal)"
		case m.Sub("AppliesToExternalUnknown") != nil:
			externalUnknownOn = subText(m, "Enabled") == "1" && subText(m, "ReplyMessage") == "Away (external)"
		}
	}
	if !internalOn {
		t.Error("Oof Get internal message not reported enabled with its body")
	}
	if !externalUnknownOn {
		t.Error("Oof Get external-unknown message not reported enabled with its body")
	}
}

// TestSettingsOofScheduled proves a time-based Oof Set (OofState 2) records the
// schedule window and Oof Get reports state 2 with the same StartTime/EndTime.
func TestSettingsOofScheduled(t *testing.T) {
	s, _ := settingsServer(t)
	s.SetOOFStore(&stubOOFStore{})

	start := "2026-07-01T08:00:00.000Z"
	end := "2026-07-08T17:00:00.000Z"
	doSettings(t, s, settingsEl("Settings", settingsEl("Oof", settingsEl("Set",
		settingsText("OofState", oofStateScheduled),
		settingsText("StartTime", start),
		settingsText("EndTime", end),
		settingsEl("OofMessage",
			settingsEl("AppliesToInternal"),
			settingsText("Enabled", "1"),
			settingsText("ReplyMessage", "On holiday"),
			settingsText("BodyType", "Text"),
		),
	))))

	get := doSettings(t, s, settingsEl("Settings", settingsEl("Oof", settingsEl("Get"))))
	body := get.Sub("Oof").Sub("Get")
	if st := subText(body, "OofState"); st != oofStateScheduled {
		t.Fatalf("OofState = %q, want %q", st, oofStateScheduled)
	}
	if got := subText(body, "StartTime"); got != start {
		t.Errorf("StartTime = %q, want %q", got, start)
	}
	if got := subText(body, "EndTime"); got != end {
		t.Errorf("EndTime = %q, want %q", got, end)
	}
}

// TestSettingsOofDisabledWinsOverDates proves a disabled policy that still
// carries a stored schedule window reports OofState 0, not 2. This mirrors the
// canonical guard (a disabled OOF is never "Scheduled") so a phone and the
// webmail/EWS surfaces agree; without it a turned-off reply would read as armed.
func TestSettingsOofDisabledWinsOverDates(t *testing.T) {
	s, _ := settingsServer(t)
	s.SetOOFStore(&stubOOFStore{cfg: &vacation.Config{
		Enabled:   false,
		StartDate: time.Date(2026, 7, 1, 8, 0, 0, 0, time.UTC),
		EndDate:   time.Date(2026, 7, 8, 17, 0, 0, 0, time.UTC),
		Message:   "leftover",
	}})

	get := doSettings(t, s, settingsEl("Settings", settingsEl("Oof", settingsEl("Get"))))
	body := get.Sub("Oof").Sub("Get")
	if st := subText(body, "OofState"); st != oofStateDisabled {
		t.Fatalf("disabled policy with dates reported OofState %q, want %q", st, oofStateDisabled)
	}
}

// TestSettingsMultipleSubcommands proves the response mirrors one block per
// request sub-command, in request order, beneath a single top-level Status — the
// structure a client relies on to match each answer to its request.
func TestSettingsMultipleSubcommands(t *testing.T) {
	s, _ := settingsServer(t)

	body := settingsEl("Settings",
		settingsEl("DeviceInformation", settingsEl("Set", settingsText("Model", "Pixel 8"))),
		settingsEl("DevicePassword", settingsEl("Set", settingsText("Password", "0000"))),
	)
	resp := doSettings(t, s, body)

	if got := subText(resp, "Status"); got != settingsStatusSuccess {
		t.Fatalf("top Status = %q, want 1", got)
	}
	var order []string
	for _, c := range resp.Children {
		order = append(order, c.Name)
	}
	want := []string{"Status", "DeviceInformation", "DevicePassword"}
	if len(order) != len(want) {
		t.Fatalf("response blocks = %v, want %v", order, want)
	}
	for i := range want {
		if order[i] != want[i] {
			t.Fatalf("response block %d = %q, want %q (order %v)", i, order[i], want[i], order)
		}
	}
}
