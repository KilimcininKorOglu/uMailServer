package activesync

import (
	"crypto/rand"
	"encoding/binary"
	"errors"
	"net/http"
	"strconv"

	"github.com/umailserver/umailserver/internal/activesync/wbxml"
	"github.com/umailserver/umailserver/internal/db"
)

// policyType is the provisioning policy format ActiveSync 12.0+ negotiates
// (MS-ASPROV); the WBXML-encoded EASProvisionDoc rides under it.
const policyType = "MS-EAS-Provisioning-WBXML"

// Provision status codes (MS-ASPROV 2.2.2.54.2): 1 = success, 2 = protocol error.
const (
	provStatusSuccess  = "1"
	provStatusProtoErr = "2"
)

// statusProvisioningRequired is HTTP 449 "Retry after sending a PROVISION
// command" (MS-ASHTTP 2.2.2): every command except Provision returns it to an
// unprovisioned, stale-key or wipe-flagged device so the client (re)runs
// Provision. It is also how an admin-requested remote wipe reaches the device.
const statusProvisioningRequired = 449

// deviceProvisioned reports whether the request carries the device's current
// policy key and the device is not pending a remote wipe — the MS-ASHTTP
// provisioning gate applied to every command except Provision. A false result
// means the caller answers 449 so the client (re)runs Provision. With no device
// store wired the gate is open: a deployment that does not persist partnerships
// cannot enforce a policy. An empty or "0" key is the unprovisioned sentinel.
func (s *Server) deviceProvisioned(r *http.Request, email string) bool {
	if s.devices == nil {
		return true
	}
	key := r.Header.Get("X-MS-PolicyKey")
	if key == "" || key == "0" {
		return false
	}
	dev, err := s.devices.GetEASDevice(email, r.URL.Query().Get("DeviceId"))
	if err != nil || dev == nil {
		return false
	}
	if dev.WipeRequested {
		return false // force the device back to Provision to receive the wipe
	}
	return dev.PolicyKey != "" && dev.PolicyKey == key
}

// handleProvision runs the MS-ASPROV two-phase policy handshake. An initial
// request (no acknowledged PolicyKey) gets a temporary key plus the policy
// document; the follow-up request that echoes that key is answered with the
// final key. The device's current key is persisted so later commands can be
// validated against it. The policy is permissive (no device-side lockdown) —
// the server gates access through account auth, not device restrictions.
func (s *Server) handleProvision(ctx *Context) ([]byte, error) {
	if s.devices == nil {
		return nil, errors.New("activesync: device store not configured")
	}
	deviceID := ctx.Request.URL.Query().Get("DeviceId")
	if deviceID == "" {
		return marshalProvision(provStatusProtoErr, "", false)
	}

	root, err := wbxml.Unmarshal(ctx.Body)
	if err != nil {
		return nil, err
	}

	// A pending remote wipe pre-empts the policy handshake: the flagged device is
	// driven through the wipe directive and acknowledgment before any (re)issue.
	if dev, err := s.devices.GetEASDevice(ctx.Email, deviceID); err == nil && dev != nil && dev.WipeRequested {
		return s.handleRemoteWipe(ctx, dev, root)
	}

	ackKey := acknowledgedPolicyKey(root)

	if ackKey == "" {
		// Phase 1: issue a temporary key and hand back the policy document.
		key, err := newPolicyKey()
		if err != nil {
			return nil, err
		}
		dev := &db.EASDevice{
			Email:           ctx.Email,
			DeviceID:        deviceID,
			DeviceType:      ctx.Request.URL.Query().Get("DeviceType"),
			UserAgent:       ctx.Request.UserAgent(),
			PolicyKey:       key,
			ProtocolVersion: clientProtocolVersion(ctx.Request),
		}
		if err := s.devices.PutEASDevice(dev); err != nil {
			return nil, err
		}
		return marshalProvision(provStatusSuccess, key, true)
	}

	// Phase 2: the client acknowledged the temporary key; confirm it matches the
	// key we issued, then rotate to a final key.
	dev, err := s.devices.GetEASDevice(ctx.Email, deviceID)
	if err != nil || dev.PolicyKey != ackKey {
		// Stale or unknown key: tell the client to restart provisioning.
		return marshalProvision(provStatusProtoErr, "", false)
	}
	key, err := newPolicyKey()
	if err != nil {
		return nil, err
	}
	dev.PolicyKey = key
	if err := s.devices.PutEASDevice(dev); err != nil {
		return nil, err
	}
	return marshalProvision(provStatusSuccess, key, false)
}

// acknowledgedPolicyKey returns the PolicyKey the client echoed in an
// acknowledgment request (Provision/Policies/Policy/PolicyKey), or "" for an
// initial request that carries no key.
func acknowledgedPolicyKey(root *wbxml.Element) string {
	if root == nil {
		return ""
	}
	policies := root.Sub("Policies")
	if policies == nil {
		return ""
	}
	policy := policies.Sub("Policy")
	if policy == nil {
		return ""
	}
	if pk := policy.Sub("PolicyKey"); pk != nil {
		return pk.Text
	}
	return ""
}

// newPolicyKey returns a fresh non-zero numeric policy key (a random 31-bit
// value as a decimal string, matching the key shape clients echo).
func newPolicyKey() (string, error) {
	var b [4]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	v := binary.BigEndian.Uint32(b[:]) & 0x7FFFFFFF
	if v == 0 {
		v = 1
	}
	return strconv.FormatUint(uint64(v), 10), nil
}

// clientProtocolVersion echoes the EAS version the client negotiated (the
// MS-ASProtocolVersion request header), defaulting to the newest supported.
func clientProtocolVersion(r *http.Request) string {
	if v := r.Header.Get("MS-ASProtocolVersion"); v != "" {
		return v
	}
	return serverVersion
}

// marshalProvision builds a Provision response: the top-level Status, and on
// success a Policy carrying the PolicyType, per-policy Status and PolicyKey —
// plus the permissive EASProvisionDoc when withDoc is set (phase 1). A non-
// success status is returned as a bare Status, prompting the client to restart.
func marshalProvision(status, policyKey string, withDoc bool) ([]byte, error) {
	root := &wbxml.Element{Page: wbxml.PageProvision, Name: "Provision", Children: []*wbxml.Element{
		{Page: wbxml.PageProvision, Name: "Status", Text: status},
	}}
	if status == provStatusSuccess {
		policy := &wbxml.Element{Page: wbxml.PageProvision, Name: "Policy", Children: []*wbxml.Element{
			{Page: wbxml.PageProvision, Name: "PolicyType", Text: policyType},
			{Page: wbxml.PageProvision, Name: "Status", Text: provStatusSuccess},
		}}
		if policyKey != "" {
			policy.Children = append(policy.Children, &wbxml.Element{Page: wbxml.PageProvision, Name: "PolicyKey", Text: policyKey})
		}
		if withDoc {
			policy.Children = append(policy.Children, &wbxml.Element{Page: wbxml.PageProvision, Name: "Data", Children: []*wbxml.Element{provisionDoc()}})
		}
		root.Children = append(root.Children, &wbxml.Element{Page: wbxml.PageProvision, Name: "Policies", Children: []*wbxml.Element{policy}})
	}
	return wbxml.Marshal(root)
}

// provisionDoc builds a permissive EASProvisionDoc: no device password and every
// capability allowed. Access control is enforced at the account-auth layer, not
// by locking down the device.
func provisionDoc() *wbxml.Element {
	field := func(name, val string) *wbxml.Element {
		return &wbxml.Element{Page: wbxml.PageProvision, Name: name, Text: val}
	}
	return &wbxml.Element{Page: wbxml.PageProvision, Name: "EASProvisionDoc", Children: []*wbxml.Element{
		field("DevicePasswordEnabled", "0"),
		field("AlphanumericDevicePasswordRequired", "0"),
		field("PasswordRecoveryEnabled", "0"),
		field("RequireStorageCardEncryption", "0"),
		field("AttachmentsEnabled", "1"),
		field("MaxInactivityTimeDeviceLock", "9999"),
		field("MaxDevicePasswordFailedAttempts", "8"),
		field("MaxAttachmentSize", "0"),
		field("AllowSimpleDevicePassword", "1"),
		field("DevicePasswordExpiration", "0"),
		field("DevicePasswordHistory", "0"),
		field("AllowStorageCard", "1"),
		field("AllowCamera", "1"),
		field("RequireDeviceEncryption", "0"),
		field("AllowUnsignedApplications", "1"),
		field("AllowUnsignedInstallationPackages", "1"),
		field("MinDevicePasswordComplexCharacters", "3"),
		field("AllowWiFi", "1"),
		field("AllowTextMessaging", "1"),
		field("AllowPOPIMAPEmail", "1"),
		field("AllowBluetooth", "2"),
		field("AllowIrDA", "1"),
		field("RequireManualSyncWhenRoaming", "0"),
		field("AllowDesktopSync", "1"),
		field("MaxCalendarAgeFilter", "0"),
		field("AllowHTMLEmail", "1"),
		field("MaxEmailAgeFilter", "0"),
		field("MaxEmailBodyTruncationSize", "-1"),
		field("MaxEmailHTMLBodyTruncationSize", "-1"),
		field("RequireSignedSMIMEMessages", "0"),
		field("RequireEncryptedSMIMEMessages", "0"),
		field("RequireSignedSMIMEAlgorithm", "0"),
		field("RequireEncryptionSMIMEAlgorithm", "0"),
		field("AllowSMIMEEncryptionAlgorithmNegotiation", "2"),
		field("AllowSMIMESoftCerts", "1"),
		field("AllowBrowser", "1"),
		field("AllowConsumerEmail", "1"),
		field("AllowRemoteDesktop", "1"),
		field("AllowInternetSharing", "1"),
	}}
}

// handleRemoteWipe drives the MS-ASPROV remote-wipe exchange for a flagged
// device. The first Provision gets a RemoteWipe directive; once the client
// acknowledges the completed wipe (Provision/RemoteWipe/Status), the partnership
// is deleted so a device that ever returns must provision from scratch. Both
// rounds answer Status 1 + RemoteWipe; only the acknowledgment removes the
// device.
func (s *Server) handleRemoteWipe(ctx *Context, dev *db.EASDevice, root *wbxml.Element) ([]byte, error) {
	if remoteWipeAcknowledged(root) {
		if err := s.devices.DeleteEASDevice(ctx.Email, dev.DeviceID); err != nil {
			return nil, err
		}
	}
	return marshalRemoteWipe()
}

// remoteWipeAcknowledged reports whether a Provision request acknowledges a
// completed wipe: a RemoteWipe element carrying a Status (MS-ASPROV 2.2.2.44).
func remoteWipeAcknowledged(root *wbxml.Element) bool {
	if root == nil {
		return false
	}
	rw := root.Sub("RemoteWipe")
	return rw != nil && rw.Sub("Status") != nil
}

// marshalRemoteWipe builds the RemoteWipe response (MS-ASPROV): the top-level
// Status 1 plus an empty RemoteWipe element — the directive telling the device
// to wipe, and the confirmation on the acknowledgment round.
func marshalRemoteWipe() ([]byte, error) {
	root := &wbxml.Element{Page: wbxml.PageProvision, Name: "Provision", Children: []*wbxml.Element{
		{Page: wbxml.PageProvision, Name: "Status", Text: provStatusSuccess},
		{Page: wbxml.PageProvision, Name: "RemoteWipe"},
	}}
	return wbxml.Marshal(root)
}
