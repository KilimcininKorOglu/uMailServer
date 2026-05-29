// Package semcore defines the canonical semantic-core identity and lifecycle
// contract for uMailServer.
//
// This file provides the canonical policy models for inbox rules, out-of-office
// (OOF) auto-reply, resource/room booking, and notification policies. These
// models are the authoritative source of truth for policy identity, version,
// execution semantics, and lifecycle across REST/admin APIs, EWS projections,
// Sieve runtime execution, and future Exchange-semantic surfaces.
//
// # Design Rules
//
//   - All IDs are opaque; only equality comparisons are meaningful.
//   - ChangeKey advances on every semantically-visible policy mutation.
//   - A stale ChangeKey on write must be rejected explicitly (version conflict).
//   - The canonical policy state is durable and can regenerate runtime execution
//     artifacts (e.g., Sieve scripts) at any time.
//   - Runtime execution (Sieve interpreter) consumes compiled artifacts, but the
//     canonical model is the source of truth for user-visible policy state.
package semcore

import (
	"encoding/json"
	"errors"
	"time"
)

// ---------------------------------------------------------------------------
// Rule identity and version
// ---------------------------------------------------------------------------

// RuleId is the authoritative identity for an inbox rule. It is assigned at
// creation and must remain stable for the rule's lifetime. Two RuleIds with
// the same raw value refer to the same logical rule within a mailbox.
type RuleId struct {
	raw string
}

// NewRuleId constructs a RuleId from its raw string representation.
// The raw value must be non-empty; empty values are treated as a nil ID.
func NewRuleId(raw string) (RuleId, error) {
	if raw == "" {
		return RuleId{}, errors.New("RuleId: empty value")
	}
	return RuleId{raw: raw}, nil
}

// MustRuleId constructs a RuleId and panics on invalid input.
// Use only in tests or trusted initialization code.
func MustRuleId(raw string) RuleId {
	id, err := NewRuleId(raw)
	if err != nil {
		panic(err)
	}
	return id
}

// String returns the raw string value. Clients must treat this as opaque.
func (id RuleId) String() string { return id.raw }

// IsZero returns true for a nil/empty RuleId.
func (id RuleId) IsZero() bool { return id.raw == "" }

// Equal reports whether two RuleIds have the same raw value.
func (id RuleId) Equal(other RuleId) bool { return id.raw == other.raw }

// MarshalJSON serializes a RuleId to its raw string value.
func (id RuleId) MarshalJSON() ([]byte, error) { return json.Marshal(id.raw) }

// UnmarshalJSON deserializes a RuleId from its raw string value.
func (id *RuleId) UnmarshalJSON(data []byte) error {
	var raw string
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	if raw == "" {
		*id = RuleId{}
		return nil
	}
	*id = RuleId{raw: raw}
	return nil
}

// ---------------------------------------------------------------------------

// RuleChangeKey is an opaque version token that advances on every semantically
// visible rule mutation. Clients must treat it as opaque and compare for
// equality only. A stale RuleChangeKey on write must be rejected explicitly
// (version conflict).
type RuleChangeKey struct {
	raw string
}

// NewRuleChangeKey constructs a RuleChangeKey from its raw string representation.
func NewRuleChangeKey(raw string) (RuleChangeKey, error) {
	if raw == "" {
		return RuleChangeKey{}, errors.New("RuleChangeKey: empty value")
	}
	return RuleChangeKey{raw: raw}, nil
}

// MustRuleChangeKey constructs a RuleChangeKey and panics on invalid input.
func MustRuleChangeKey(raw string) RuleChangeKey {
	ck, err := NewRuleChangeKey(raw)
	if err != nil {
		panic(err)
	}
	return ck
}

// String returns the raw string value.
func (ck RuleChangeKey) String() string { return ck.raw }

// IsZero returns true for a nil/empty RuleChangeKey.
func (ck RuleChangeKey) IsZero() bool { return ck.raw == "" }

// Equal reports whether two RuleChangeKeys have the same raw value.
func (ck RuleChangeKey) Equal(other RuleChangeKey) bool { return ck.raw == other.raw }

// MarshalJSON serializes a RuleChangeKey to its raw string value.
func (ck RuleChangeKey) MarshalJSON() ([]byte, error) { return json.Marshal(ck.raw) }

// UnmarshalJSON deserializes a RuleChangeKey from its raw string value.
func (ck *RuleChangeKey) UnmarshalJSON(data []byte) error {
	var raw string
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	if raw == "" {
		*ck = RuleChangeKey{}
		return nil
	}
	*ck = RuleChangeKey{raw: raw}
	return nil
}

// ---------------------------------------------------------------------------
// Rule condition model
// ---------------------------------------------------------------------------

// RuleConditionKind identifies the kind of condition test.
type RuleConditionKind uint8

const (
	RuleConditionKindFrom    RuleConditionKind = iota // header "From" contains
	RuleConditionKindTo                               // header "To" contains
	RuleConditionKindSubject                          // header "Subject" contains
	RuleConditionKindBody                             // body contains
	RuleConditionKindHeader                           // arbitrary header contains
	RuleConditionKindSize                             // size over/under threshold
	RuleConditionKindFlag                             // message has/hasn't flag
	RuleConditionKindAddress                          // From/To/CC contains address
)

// RuleMatchType is the string comparison operator.
type RuleMatchType string

const (
	RuleMatchTypeContains   RuleMatchType = "contains"
	RuleMatchTypeEquals     RuleMatchType = "equals"
	RuleMatchTypeStartsWith RuleMatchType = "startsWith"
	RuleMatchTypeEndsWith   RuleMatchType = "endsWith"
	RuleMatchTypeMatches    RuleMatchType = "matches" // regex
)

// RuleCondition is a single condition in an inbox rule.
// All conditions within one Rule must match (AND logic) unless Rule.MatchAll is false.
type RuleCondition struct {
	Kind       RuleConditionKind `json:"kind"`
	MatchType  RuleMatchType     `json:"matchType"`
	Value      string            `json:"value"`
	HeaderName string            `json:"headerName,omitempty"` // for Kind == Header
}

// IsZero returns true for an uninitialized condition.
func (c *RuleCondition) IsZero() bool {
	return c.Kind == 0 && c.MatchType == "" && c.Value == ""
}

// ---------------------------------------------------------------------------
// Rule action model
// ---------------------------------------------------------------------------

// RuleActionKind identifies the action to perform.
type RuleActionKind uint8

const (
	RuleActionKindMoveToFolder        RuleActionKind = iota // move message to folder
	RuleActionKindCopyToFolder                              // copy message to folder
	RuleActionKindDelete                                    // delete message
	RuleActionKindMarkRead                                  // mark as read
	RuleActionKindMarkImportant                             // mark as important/starred
	RuleActionKindForward                                   // forward to address
	RuleActionKindForwardAsAttachment                       // forward to address as attachment
	RuleActionKindRedirect                                  // redirect to address
	RuleActionKindReject                                    // reject with message
	RuleActionKindVacation                                  // send vacation auto-reply
	RuleActionKindAddHeader                                 // add header
	RuleActionKindDeleteHeader                              // remove header
	RuleActionKindFlag                                      // set/clear flag
	RuleActionKindStop                                      // stop processing rules
)

// String returns a human-readable label for the action kind.
func (k RuleActionKind) String() string {
	switch k {
	case RuleActionKindMoveToFolder:
		return "moveToFolder"
	case RuleActionKindCopyToFolder:
		return "copyToFolder"
	case RuleActionKindDelete:
		return "delete"
	case RuleActionKindMarkRead:
		return "markRead"
	case RuleActionKindMarkImportant:
		return "markImportant"
	case RuleActionKindForward:
		return "forward"
	case RuleActionKindForwardAsAttachment:
		return "forwardAsAttachment"
	case RuleActionKindRedirect:
		return "redirect"
	case RuleActionKindReject:
		return "reject"
	case RuleActionKindVacation:
		return "vacation"
	case RuleActionKindAddHeader:
		return "addHeader"
	case RuleActionKindDeleteHeader:
		return "deleteHeader"
	case RuleActionKindFlag:
		return "flag"
	case RuleActionKindStop:
		return "stop"
	default:
		return "unknown"
	}
}

// RuleAction is a single action to perform when the rule matches.
type RuleAction struct {
	Kind        RuleActionKind `json:"kind"`
	Target      string         `json:"target,omitempty"`      // folder path for move/copy
	ForwardTo   string         `json:"forwardTo,omitempty"`   // email for forward/redirect
	Message     string         `json:"message,omitempty"`     // rejection message
	HeaderName  string         `json:"headerName,omitempty"`  // for add/delete header
	HeaderValue string         `json:"headerValue,omitempty"` // for add header
	FlagName    string         `json:"flagName,omitempty"`    // for flag action
	ClearFlag   bool           `json:"clearFlag,omitempty"`   // for flag: true = clear, false = set
}

// IsZero returns true for an uninitialized action.
func (a *RuleAction) IsZero() bool {
	return a.Kind == 0 && a.Target == "" && a.ForwardTo == ""
}

// ---------------------------------------------------------------------------
// Rule
//
// Canonical inbox rule. RuleId is scoped to a MailboxId. The rule has a
// priority order; lower priority number = higher precedence (evaluated first).
// A Rule carries a RuleChangeKey that advances on every user-visible change.
// ---------------------------------------------------------------------------

// Rule is the canonical representation of an inbox rule. It holds the
// user-visible policy state: conditions, actions, enabled state, and order.
// The Sieve script is a compiled projection of this canonical state and can
// be regenerated at any time.
type Rule struct {
	// Identity
	ID        RuleId        `json:"id"`
	MailboxID MailboxId     `json:"mailboxId"`
	ChangeKey RuleChangeKey `json:"changeKey"`

	// Name and state
	Name     string `json:"name"`
	Enabled  bool   `json:"enabled"`
	Priority int    `json:"priority"` // Lower = higher precedence; evaluated first

	// Matching
	MatchAll   bool            `json:"matchAll"` // true = all conditions must match; false = any
	Conditions []RuleCondition `json:"conditions"`
	Actions    []RuleAction    `json:"actions"`

	// Lifecycle
	Created  time.Time `json:"created"`
	Modified time.Time `json:"modified"`
}

// IsZero returns true for a nil/uninitialized Rule.
func (r *Rule) IsZero() bool { return r.ID.IsZero() }

// ---------------------------------------------------------------------------
// OOF identity and version
// ---------------------------------------------------------------------------

// OOFId is the authoritative identity for an out-of-office policy.
// There is exactly one OOFId per mailbox; the ID is the MailboxId.
type OOFId struct {
	raw string
}

// NewOOFId constructs an OOFId from its raw string representation.
func NewOOFId(raw string) (OOFId, error) {
	if raw == "" {
		return OOFId{}, errors.New("OOFId: empty value")
	}
	return OOFId{raw: raw}, nil
}

// MustOOFId constructs an OOFId and panics on invalid input.
func MustOOFId(raw string) OOFId {
	id, err := NewOOFId(raw)
	if err != nil {
		panic(err)
	}
	return id
}

// String returns the raw string value.
func (id OOFId) String() string { return id.raw }

// IsZero returns true for a nil/empty OOFId.
func (id OOFId) IsZero() bool { return id.raw == "" }

// Equal reports whether two OOFIds have the same raw value.
func (id OOFId) Equal(other OOFId) bool { return id.raw == other.raw }

// MarshalJSON serializes an OOFId to its raw string value.
func (id OOFId) MarshalJSON() ([]byte, error) { return json.Marshal(id.raw) }

// UnmarshalJSON deserializes an OOFId from its raw string value.
func (id *OOFId) UnmarshalJSON(data []byte) error {
	var raw string
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	if raw == "" {
		*id = OOFId{}
		return nil
	}
	*id = OOFId{raw: raw}
	return nil
}

// ---------------------------------------------------------------------------

// OOFChangeKey is an opaque version token that advances on every OOF policy change.
type OOFChangeKey struct {
	raw string
}

// NewOOFChangeKey constructs an OOFChangeKey from its raw string representation.
func NewOOFChangeKey(raw string) (OOFChangeKey, error) {
	if raw == "" {
		return OOFChangeKey{}, errors.New("OOFChangeKey: empty value")
	}
	return OOFChangeKey{raw: raw}, nil
}

// MustOOFChangeKey constructs an OOFChangeKey and panics on invalid input.
func MustOOFChangeKey(raw string) OOFChangeKey {
	ck, err := NewOOFChangeKey(raw)
	if err != nil {
		panic(err)
	}
	return ck
}

// String returns the raw string value.
func (ck OOFChangeKey) String() string { return ck.raw }

// IsZero returns true for a nil/empty OOFChangeKey.
func (ck OOFChangeKey) IsZero() bool { return ck.raw == "" }

// Equal reports whether two OOFChangeKeys have the same raw value.
func (ck OOFChangeKey) Equal(other OOFChangeKey) bool { return ck.raw == other.raw }

// MarshalJSON serializes an OOFChangeKey to its raw string value.
func (ck OOFChangeKey) MarshalJSON() ([]byte, error) { return json.Marshal(ck.raw) }

// UnmarshalJSON deserializes an OOFChangeKey from its raw string value.
func (ck *OOFChangeKey) UnmarshalJSON(data []byte) error {
	var raw string
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	if raw == "" {
		*ck = OOFChangeKey{}
		return nil
	}
	*ck = OOFChangeKey{raw: raw}
	return nil
}

// ---------------------------------------------------------------------------
// OOF audience and suppression
// ---------------------------------------------------------------------------

// OOFAudience defines the recipients that receive OOF replies.
type OOFAudience uint8

const (
	OOFAudienceInternal OOFAudience = iota // only internal domain recipients
	OOFAudienceExternal                    // all known external recipients
	OOFAudienceEveryone                    // everyone including unknown
)

// OOFAudienceFromString parses an audience string.
func OOFAudienceFromString(s string) OOFAudience {
	switch s {
	case "internal":
		return OOFAudienceInternal
	case "external":
		return OOFAudienceExternal
	case "everyone":
		return OOFAudienceEveryone
	default:
		return OOFAudienceInternal
	}
}

// String returns the audience string for serialization.
func (a OOFAudience) String() string {
	switch a {
	case OOFAudienceInternal:
		return "internal"
	case OOFAudienceExternal:
		return "external"
	case OOFAudienceEveryone:
		return "everyone"
	default:
		return "internal"
	}
}

// OOFAutoReplyStyle is how the OOF reply is sent.
type OOFAutoReplyStyle uint8

const (
	OOFAutoReplyStyleText OOFAutoReplyStyle = iota // plain text body
	OOFAutoReplyStyleHTML                          // HTML body
	OOFAutoReplyStyleBoth                          // both text and HTML
)

// ---------------------------------------------------------------------------
// OOF (Out-of-Office) policy
//
// Canonical OOF policy. OOFId is the MailboxId (one OOF per mailbox).
// The OOF policy is the durable source of truth; Sieve vacation action
// is a compiled projection of this policy.
// ---------------------------------------------------------------------------

// OOFPolicy is the canonical representation of an out-of-office auto-reply
// policy. It holds the user-visible state: enabled, schedule, content,
// audience, and suppression rules.
type OOFPolicy struct {
	// Identity
	ID        OOFId        `json:"id"`
	MailboxID MailboxId    `json:"mailboxId"`
	ChangeKey OOFChangeKey `json:"changeKey"`

	// Enabled state
	Enabled bool `json:"enabled"`

	// Schedule (timezone-aware)
	StartTime time.Time `json:"startTime,omitempty"` // zero = no start restriction
	EndTime   time.Time `json:"endTime,omitempty"`   // zero = no end restriction
	Timezone  string    `json:"timezone,omitempty"`  // IANA tz name e.g. "America/New_York"

	// Reply content
	Subject    string            `json:"subject"`
	TextBody   string            `json:"textBody"`
	HTMLBody   string            `json:"htmlBody,omitempty"`
	ReplyStyle OOFAutoReplyStyle `json:"replyStyle"`

	// Audience and suppression
	Audience          OOFAudience `json:"audience"`                   // who receives replies
	ExcludeAddresses  []string    `json:"excludeAddresses,omitempty"` // do not reply to these
	IgnoreLists       bool        `json:"ignoreLists"`                // don't reply to List-ID mailing lists
	IgnoreBulk        bool        `json:"ignoreBulk"`                 // don't reply to bulk/promotional
	IgnoreAutoReplies bool        `json:"ignoreAutoReplies"`          // don't reply to Auto-Submitted:

	// Resend interval (minimum seconds between replies to same sender)
	SendIntervalSeconds int64 `json:"sendIntervalSeconds"` // 0 = use server default (7 days)
}

// IsActiveNow returns true if OOF is currently active based on schedule.
func (p *OOFPolicy) IsActiveNow() bool {
	if !p.Enabled {
		return false
	}
	now := time.Now()
	if !p.StartTime.IsZero() && now.Before(p.StartTime) {
		return false
	}
	if !p.EndTime.IsZero() && now.After(p.EndTime) {
		return false
	}
	return true
}

// SendInterval returns the effective send interval as a duration.
func (p *OOFPolicy) SendInterval() time.Duration {
	if p.SendIntervalSeconds <= 0 {
		return 7 * 24 * time.Hour // default 7 days
	}
	return time.Duration(p.SendIntervalSeconds) * time.Second
}

// IsZero returns true for a nil/uninitialized OOFPolicy.
func (p *OOFPolicy) IsZero() bool { return p.ID.IsZero() }

// ---------------------------------------------------------------------------
// Resource identity and version
// ---------------------------------------------------------------------------

// ResourceId is the authoritative identity for a room or equipment resource.
// It is assigned at creation and must remain stable for the resource's lifetime.
type ResourceId struct {
	raw string
}

// NewResourceId constructs a ResourceId from its raw string representation.
func NewResourceId(raw string) (ResourceId, error) {
	if raw == "" {
		return ResourceId{}, errors.New("ResourceId: empty value")
	}
	return ResourceId{raw: raw}, nil
}

// MustResourceId constructs a ResourceId and panics on invalid input.
func MustResourceId(raw string) ResourceId {
	id, err := NewResourceId(raw)
	if err != nil {
		panic(err)
	}
	return id
}

// String returns the raw string value.
func (id ResourceId) String() string { return id.raw }

// IsZero returns true for a nil/empty ResourceId.
func (id ResourceId) IsZero() bool { return id.raw == "" }

// Equal reports whether two ResourceIds have the same raw value.
func (id ResourceId) Equal(other ResourceId) bool { return id.raw == other.raw }

// MarshalJSON serializes a ResourceId to its raw string value.
func (id ResourceId) MarshalJSON() ([]byte, error) { return json.Marshal(id.raw) }

// UnmarshalJSON deserializes a ResourceId from its raw string value.
func (id *ResourceId) UnmarshalJSON(data []byte) error {
	var raw string
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	if raw == "" {
		*id = ResourceId{}
		return nil
	}
	*id = ResourceId{raw: raw}
	return nil
}

// ---------------------------------------------------------------------------

// ResourceChangeKey is an opaque version token that advances on every
// resource policy change.
type ResourceChangeKey struct {
	raw string
}

// NewResourceChangeKey constructs a ResourceChangeKey from its raw string representation.
func NewResourceChangeKey(raw string) (ResourceChangeKey, error) {
	if raw == "" {
		return ResourceChangeKey{}, errors.New("ResourceChangeKey: empty value")
	}
	return ResourceChangeKey{raw: raw}, nil
}

// MustResourceChangeKey constructs a ResourceChangeKey and panics on invalid input.
func MustResourceChangeKey(raw string) ResourceChangeKey {
	ck, err := NewResourceChangeKey(raw)
	if err != nil {
		panic(err)
	}
	return ck
}

// String returns the raw string value.
func (ck ResourceChangeKey) String() string { return ck.raw }

// IsZero returns true for a nil/empty ResourceChangeKey.
func (ck ResourceChangeKey) IsZero() bool { return ck.raw == "" }

// Equal reports whether two ResourceChangeKeys have the same raw value.
func (ck ResourceChangeKey) Equal(other ResourceChangeKey) bool { return ck.raw == other.raw }

// MarshalJSON serializes a ResourceChangeKey to its raw string value.
func (ck ResourceChangeKey) MarshalJSON() ([]byte, error) { return json.Marshal(ck.raw) }

// UnmarshalJSON deserializes a ResourceChangeKey from its raw string value.
func (ck *ResourceChangeKey) UnmarshalJSON(data []byte) error {
	var raw string
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	if raw == "" {
		*ck = ResourceChangeKey{}
		return nil
	}
	*ck = ResourceChangeKey{raw: raw}
	return nil
}

// ---------------------------------------------------------------------------
// Resource type
// ---------------------------------------------------------------------------

// ResourceKind classifies the resource type.
type ResourceKind uint8

const (
	ResourceKindRoom      ResourceKind = iota // meeting room
	ResourceKindEquipment                     // projector, laptop, etc.
)

// String returns a human-readable label for the resource kind.
func (k ResourceKind) String() string {
	switch k {
	case ResourceKindRoom:
		return "room"
	case ResourceKindEquipment:
		return "equipment"
	default:
		return "unknown"
	}
}

// ---------------------------------------------------------------------------
// Booking policy
// ---------------------------------------------------------------------------

// BookingDecision is what the resource does with a booking request.
type BookingDecision uint8

const (
	BookingDecisionAutoAccept     BookingDecision = iota // auto-accept if no conflict
	BookingDecisionAutoDecline                           // auto-decline all
	BookingDecisionDelegateReview                        // route to delegate for review
	BookingDecisionProvisional                           // provisional accept, needs confirmation
)

// ---------------------------------------------------------------------------
// Resource policy
//
// Canonical resource/room policy. ResourceId is the authoritative resource identity.
// The policy is the durable source of truth for booking behavior, capacity,
// and delegate routing.
// ---------------------------------------------------------------------------

// ResourcePolicy is the canonical representation of a room or equipment resource
// booking policy.
type ResourcePolicy struct {
	// Identity
	ID        ResourceId        `json:"id"`
	MailboxID MailboxId         `json:"mailboxId"`
	ChangeKey ResourceChangeKey `json:"changeKey"`

	// Resource metadata
	Name        string       `json:"name"`
	Kind        ResourceKind `json:"kind"`
	Email       string       `json:"email"`    // resource mailbox email
	Capacity    int          `json:"capacity"` // for rooms
	Description string       `json:"description"`

	// Booking behavior
	Decision           BookingDecision `json:"decision"`                // auto-accept/decline/delegate
	DelegateEmail      string          `json:"delegateEmail,omitempty"` // for delegate review
	AllowRecurring     bool            `json:"allowRecurring"`          // allow recurring meetings
	MaxDurationMinutes int             `json:"maxDurationMinutes"`      // maximum meeting duration
	MinNoticeMinutes   int             `json:"minNoticeMinutes"`        // minimum advance booking

	// Conflict handling
	AllowConflicts bool `json:"allowConflicts"` // allow overlapping bookings
	MaxConflicts   int  `json:"maxConflicts"`   // max conflicts before decline

	// Visibility
	HiddenFromGAL bool `json:"hiddenFromGAL"` // don't show in room finder

	// Lifecycle
	Created  time.Time `json:"created"`
	Modified time.Time `json:"modified"`
}

// IsZero returns true for a nil/uninitialized ResourcePolicy.
func (r *ResourcePolicy) IsZero() bool { return r.ID.IsZero() }

// ---------------------------------------------------------------------------
// Notification identity and version
// ---------------------------------------------------------------------------

// NotificationId is the authoritative identity for a notification policy.
type NotificationId struct {
	raw string
}

// NewNotificationId constructs a NotificationId from its raw string representation.
func NewNotificationId(raw string) (NotificationId, error) {
	if raw == "" {
		return NotificationId{}, errors.New("NotificationId: empty value")
	}
	return NotificationId{raw: raw}, nil
}

// MustNotificationId constructs a NotificationId and panics on invalid input.
func MustNotificationId(raw string) NotificationId {
	id, err := NewNotificationId(raw)
	if err != nil {
		panic(err)
	}
	return id
}

// String returns the raw string value.
func (id NotificationId) String() string { return id.raw }

// IsZero returns true for a nil/empty NotificationId.
func (id NotificationId) IsZero() bool { return id.raw == "" }

// Equal reports whether two NotificationIds have the same raw value.
func (id NotificationId) Equal(other NotificationId) bool { return id.raw == other.raw }

// MarshalJSON serializes a NotificationId to its raw string value.
func (id NotificationId) MarshalJSON() ([]byte, error) { return json.Marshal(id.raw) }

// UnmarshalJSON deserializes a NotificationId from its raw string value.
func (id *NotificationId) UnmarshalJSON(data []byte) error {
	var raw string
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	if raw == "" {
		*id = NotificationId{}
		return nil
	}
	*id = NotificationId{raw: raw}
	return nil
}

// ---------------------------------------------------------------------------

// NotificationChangeKey is an opaque version token for notification policies.
type NotificationChangeKey struct {
	raw string
}

// NewNotificationChangeKey constructs a NotificationChangeKey from its raw string representation.
func NewNotificationChangeKey(raw string) (NotificationChangeKey, error) {
	if raw == "" {
		return NotificationChangeKey{}, errors.New("NotificationChangeKey: empty value")
	}
	return NotificationChangeKey{raw: raw}, nil
}

// MustNotificationChangeKey constructs a NotificationChangeKey and panics on invalid input.
func MustNotificationChangeKey(raw string) NotificationChangeKey {
	ck, err := NewNotificationChangeKey(raw)
	if err != nil {
		panic(err)
	}
	return ck
}

// String returns the raw string value.
func (ck NotificationChangeKey) String() string { return ck.raw }

// IsZero returns true for a nil/empty NotificationChangeKey.
func (ck NotificationChangeKey) IsZero() bool { return ck.raw == "" }

// Equal reports whether two NotificationChangeKeys have the same raw value.
func (ck NotificationChangeKey) Equal(other NotificationChangeKey) bool { return ck.raw == other.raw }

// MarshalJSON serializes a NotificationChangeKey to its raw string value.
func (ck NotificationChangeKey) MarshalJSON() ([]byte, error) { return json.Marshal(ck.raw) }

// UnmarshalJSON deserializes a NotificationChangeKey from its raw string value.
func (ck *NotificationChangeKey) UnmarshalJSON(data []byte) error {
	var raw string
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	if raw == "" {
		*ck = NotificationChangeKey{}
		return nil
	}
	*ck = NotificationChangeKey{raw: raw}
	return nil
}

// ---------------------------------------------------------------------------
// Notification trigger and delivery
// ---------------------------------------------------------------------------

// NotificationTriggerKind identifies when a notification fires.
type NotificationTriggerKind uint8

const (
	NotificationTriggerImmediate NotificationTriggerKind = iota // immediately on event
	NotificationTriggerAtStart                                  // at event start time
	NotificationTriggerAtEnd                                    // at event end time
	NotificationTriggerBefore                                   // N minutes before
	NotificationTriggerAfter                                    // N minutes after
)

// NotificationDeliveryMethod is how the notification is sent.
type NotificationDeliveryMethod uint8

const (
	NotificationDeliveryEmail NotificationDeliveryMethod = iota // email
	NotificationDeliveryPush                                    // push notification
	NotificationDeliverySMS                                     // SMS
)

// NotificationPolicy is the canonical representation of a notification delivery
// policy for calendar items, tasks, or inbox rules.
type NotificationPolicy struct {
	// Identity
	ID        NotificationId        `json:"id"`
	MailboxID MailboxId             `json:"mailboxId"`
	ChangeKey NotificationChangeKey `json:"changeKey"`

	// Trigger
	TriggerKind   NotificationTriggerKind `json:"triggerKind"`
	MinutesBefore int                     `json:"minutesBefore"` // for Before trigger

	// Delivery
	Delivery NotificationDeliveryMethod `json:"delivery"`

	// Scope
	ItemID   ItemId `json:"itemId,omitempty"` // for per-item override
	RuleID   RuleId `json:"ruleId,omitempty"` // for rule-triggered notification
	Disabled bool   `json:"disabled"`         // opt-out globally

	// Lifecycle
	Created  time.Time `json:"created"`
	Modified time.Time `json:"modified"`
}

// IsZero returns true for a nil/uninitialized NotificationPolicy.
func (n *NotificationPolicy) IsZero() bool { return n.ID.IsZero() }
