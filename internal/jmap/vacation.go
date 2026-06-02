package jmap

import (
	"fmt"
	"time"

	"github.com/umailserver/umailserver/internal/semcore"
)

// JMAP VacationResponse (RFC 8621 §8) is backed by the canonical semcore OOF
// policy — the same store EWS SetUserOofSettings and the webmail vacation
// endpoints use — so an out-of-office reply configured over JMAP is identical
// across every surface and fires at delivery via the recompiled Sieve script.
// There is exactly one VacationResponse per account, with id "singleton".

const vacationSingletonID = "singleton"

// argString/argMap/argSlice read a JMAP method argument with a checked type
// assertion, returning the zero value when absent or of the wrong type.
func argString(args map[string]interface{}, key string) string {
	if v, ok := args[key].(string); ok {
		return v
	}
	return ""
}

func argMap(args map[string]interface{}, key string) map[string]interface{} {
	if v, ok := args[key].(map[string]interface{}); ok {
		return v
	}
	return nil
}

func argSlice(args map[string]interface{}, key string) []interface{} {
	if v, ok := args[key].([]interface{}); ok {
		return v
	}
	return nil
}

// oofToVacationResponse projects the canonical OOF policy onto the JMAP
// VacationResponse object. Nil/zero values are emitted as JSON null.
func oofToVacationResponse(policy *semcore.OOFPolicy) map[string]interface{} {
	obj := map[string]interface{}{
		"id":        vacationSingletonID,
		"isEnabled": false,
		"fromDate":  nil,
		"toDate":    nil,
		"subject":   nil,
		"textBody":  nil,
		"htmlBody":  nil,
	}
	if policy == nil || policy.IsZero() {
		return obj
	}
	obj["isEnabled"] = policy.Enabled
	if !policy.StartTime.IsZero() {
		obj["fromDate"] = policy.StartTime.UTC().Format(time.RFC3339)
	}
	if !policy.EndTime.IsZero() {
		obj["toDate"] = policy.EndTime.UTC().Format(time.RFC3339)
	}
	if policy.Subject != "" {
		obj["subject"] = policy.Subject
	}
	text := policy.TextBody
	if text == "" {
		text = policy.InternalReply
	}
	if text != "" {
		obj["textBody"] = text
	}
	if policy.HTMLBody != "" {
		obj["htmlBody"] = policy.HTMLBody
	}
	return obj
}

// loadOOF reads the caller's canonical OOF policy (zero policy when absent).
func (s *Server) loadOOF(user string) (*semcore.OOFPolicy, semcore.MailboxId, error) {
	mbid, err := semcore.NewMailboxId(user)
	if err != nil {
		return nil, mbid, err
	}
	oofID, err := semcore.NewOOFId(mbid.String())
	if err != nil {
		return nil, mbid, err
	}
	policy, err := s.policyStore.GetOOF(oofID)
	if err != nil || policy == nil {
		// Absent OOF is not an error: present a disabled singleton.
		return &semcore.OOFPolicy{ID: oofID, MailboxID: mbid}, mbid, nil
	}
	return policy, mbid, nil
}

func (s *Server) handleVacationResponseGet(user string, call MethodCall) Response {
	accountID := argString(call.Args, "accountId")
	if valid, resp := validateAccountId(accountID, user, "VacationResponse/get", call.ID); !valid {
		return resp
	}
	if !s.vacationEnabled() {
		return jmapError(call.ID, "notSupported", "vacation response is not available")
	}

	policy, _, err := s.loadOOF(user)
	if err != nil {
		return jmapError(call.ID, "serverFail", err.Error())
	}

	// Honor an explicit ids filter; only "singleton" exists.
	ids, hasIDs := call.Args["ids"].([]interface{})
	list := []interface{}{}
	notFound := []string{}
	if hasIDs {
		for _, raw := range ids {
			id, isStr := raw.(string)
			switch {
			case isStr && id == vacationSingletonID:
				list = append(list, oofToVacationResponse(policy))
			case isStr:
				notFound = append(notFound, id)
			}
		}
	} else {
		list = append(list, oofToVacationResponse(policy))
	}

	return Response{
		Name: "VacationResponse/get",
		Args: map[string]interface{}{
			"accountId": accountID,
			"state":     fmt.Sprintf("state-%d", time.Now().Unix()),
			"list":      list,
			"notFound":  notFound,
		},
		ID: call.ID,
	}
}

func (s *Server) handleVacationResponseSet(user string, call MethodCall) Response {
	accountID := argString(call.Args, "accountId")
	if valid, resp := validateAccountId(accountID, user, "VacationResponse/set", call.ID); !valid {
		return resp
	}
	if !s.vacationEnabled() {
		return jmapError(call.ID, "notSupported", "vacation response is not available")
	}

	create := argMap(call.Args, "create")
	update := argMap(call.Args, "update")
	destroy := argSlice(call.Args, "destroy")

	notCreated := map[string]interface{}{}
	for id := range create {
		// The singleton cannot be created or destroyed (RFC 8621 §8.3).
		notCreated[id] = map[string]interface{}{
			"type":        "singleton",
			"description": "VacationResponse is a singleton; update id \"singleton\" instead.",
		}
	}
	notDestroyed := map[string]interface{}{}
	for _, raw := range destroy {
		if id, ok := raw.(string); ok {
			notDestroyed[id] = map[string]interface{}{
				"type":        "singleton",
				"description": "VacationResponse is a singleton and cannot be destroyed.",
			}
		}
	}

	updated := map[string]interface{}{}
	notUpdated := map[string]interface{}{}
	for id, raw := range update {
		if id != vacationSingletonID {
			notUpdated[id] = map[string]interface{}{"type": "notFound"}
			continue
		}
		patch, ok := raw.(map[string]interface{})
		if !ok {
			notUpdated[id] = map[string]interface{}{"type": "invalidPatch"}
			continue
		}
		if err := s.applyVacationPatch(user, patch); err != nil {
			notUpdated[id] = map[string]interface{}{"type": "serverFail", "description": err.Error()}
			continue
		}
		updated[id] = nil
	}

	return Response{
		Name: "VacationResponse/set",
		Args: map[string]interface{}{
			"accountId":    accountID,
			"oldState":     nil,
			"newState":     fmt.Sprintf("state-%d", time.Now().Unix()),
			"created":      map[string]interface{}{},
			"updated":      updated,
			"destroyed":    []string{},
			"notCreated":   notCreated,
			"notUpdated":   notUpdated,
			"notDestroyed": notDestroyed,
		},
		ID: call.ID,
	}
}

// applyVacationPatch merges a JMAP VacationResponse patch onto the caller's
// canonical OOF policy, persists it, and recompiles the Sieve script so the
// change takes effect at delivery.
func (s *Server) applyVacationPatch(user string, patch map[string]interface{}) error {
	policy, mbid, err := s.loadOOF(user)
	if err != nil {
		return err
	}
	oofID, err := semcore.NewOOFId(mbid.String())
	if err != nil {
		return err
	}
	policy.ID = oofID
	policy.MailboxID = mbid
	if policy.Timezone == "" {
		policy.Timezone = "UTC"
	}
	if policy.SendIntervalSeconds <= 0 {
		policy.SendIntervalSeconds = 7 * 24 * 3600
	}

	if v, ok := patch["isEnabled"]; ok {
		if b, isBool := v.(bool); isBool {
			policy.Enabled = b
		}
	}
	if v, ok := patch["subject"]; ok {
		if sv, isStr := v.(string); isStr {
			policy.Subject = sv
		}
	}
	if v, ok := patch["textBody"]; ok {
		if sv, isStr := v.(string); isStr {
			policy.TextBody = sv
			policy.InternalReply = sv
		}
	}
	if v, ok := patch["htmlBody"]; ok {
		if sv, isStr := v.(string); isStr {
			policy.HTMLBody = sv
		}
	}
	if v, ok := patch["fromDate"]; ok {
		policy.StartTime = parseJMAPDate(v)
	}
	if v, ok := patch["toDate"]; ok {
		policy.EndTime = parseJMAPDate(v)
	}

	// Derive the EWS-compatible state so GetUserOofSettings round-trips it.
	switch {
	case !policy.Enabled:
		policy.State = "Disabled"
	case !policy.StartTime.IsZero() || !policy.EndTime.IsZero():
		policy.State = "Scheduled"
	default:
		policy.State = "Enabled"
	}

	if err := s.policyStore.PutOOF(policy); err != nil {
		return fmt.Errorf("persist OOF: %w", err)
	}
	if err := s.recompileSieve(user); err != nil {
		return fmt.Errorf("recompile sieve: %w", err)
	}
	return nil
}

// parseJMAPDate parses a JMAP UTCDate (RFC3339) value, returning the zero time
// for null or unparseable input (clearing the schedule bound).
func parseJMAPDate(v interface{}) time.Time {
	s, ok := v.(string)
	if !ok || s == "" {
		return time.Time{}
	}
	if t, err := time.Parse(time.RFC3339, s); err == nil {
		return t.UTC()
	}
	return time.Time{}
}

// jmapError builds a method-level error Response (RFC 8620 §3.6.1).
func jmapError(callID, errType, desc string) Response {
	return Response{
		Name: "error",
		Args: map[string]interface{}{
			"type":        errType,
			"description": desc,
		},
		ID: callID,
	}
}
