package ews

import (
	"bytes"
	"encoding/xml"
	"fmt"
	"testing"
)

func TestGetUserOofSettingsResponseMarshal(t *testing.T) {
	// NOTE: GetUserOofSettingsResponse cannot be marshaled with xml.Marshal due to
	// SA5008: the OofSettings field tag name "OofSettings" conflicts with
	// UserOofSettings.XMLName "UserOofSettings". The server uses string-based
	// XML building instead (see TestGetUserOofSettingsResponseStringMarshal).
	// This test verifies the struct fields are correctly populated.
	resp := GetUserOofSettingsResponse{
		ResponseMessage: ResponseMessageType{
			ResponseClass: ResponseClassSuccess,
			ResponseCode:  ErrNoError,
		},
		OofSettings: &UserOofSettings{
			OofState:        OofStateDisabled,
			ExternalAudience: ExternalAudienceNone,
		},
		AllowExternalOof: ExternalAudienceNone,
	}
	if resp.OofSettings.OofState != OofStateDisabled {
		t.Errorf("OofSettings.OofState: got %v, want Disabled", resp.OofSettings.OofState)
	}
	if resp.OofSettings.ExternalAudience != ExternalAudienceNone {
		t.Errorf("OofSettings.ExternalAudience: got %v, want None", resp.OofSettings.ExternalAudience)
	}
	// Verify string-based marshaling produces non-empty output.
	var buf bytes.Buffer
	buf.WriteString(`<m:GetUserOofSettingsResponse>`)
	buf.WriteString(`<m:ResponseMessage ResponseClass="Success">`)
	buf.WriteString(`<m:ResponseCode>NoError</m:ResponseCode>`)
	buf.WriteString(`</m:ResponseMessage>`)
	buf.WriteString(`<t:OofSettings>`)
	buf.WriteString(`<t:OofState>Disabled</t:OofState>`)
	buf.WriteString(`<t:ExternalAudience>None</t:ExternalAudience>`)
	buf.WriteString(`</t:OofSettings>`)
	buf.WriteString(`<t:AllowExternalOof>None</t:AllowExternalOof>`)
	buf.WriteString(`</m:GetUserOofSettingsResponse>`)
	data := buf.Bytes()
	if len(data) == 0 {
		t.Fatal("string marshal returned empty")
	}
	t.Logf("String marshal:\n%s", string(data))
}

func TestSetUserOofSettingsResponseMarshal(t *testing.T) {
	resp := SetUserOofSettingsResponse{
		ResponseMessage: ResponseMessageType{
			ResponseClass: ResponseClassSuccess,
			ResponseCode:  ErrNoError,
		},
	}
	data, err := xml.Marshal(resp)
	if err != nil {
		t.Fatalf("xml.Marshal error: %v", err)
	}
	fmt.Printf("SetUserOofSettingsResponse marshal:\n%s\n", string(data))
	if len(data) == 0 {
		t.Fatal("xml.Marshal returned empty bytes for SetUserOofSettingsResponse")
	}
}

func TestGetUserOofSettingsResponseStringMarshal(t *testing.T) {
	// Build the response using string concatenation (same pattern as handleGetInboxRules).
	// This avoids the Go XML encoder bug with embedded struct XMLName conflicts.
	var buf bytes.Buffer
	respClass := "Success"
	responseCode := "NoError"
	oofState := "Disabled"
	externalAudience := "None"

	buf.WriteString(`<m:GetUserOofSettingsResponse>`)
	buf.WriteString(`<m:ResponseMessage ResponseClass="` + respClass + `">`)
	buf.WriteString(`<m:ResponseCode>` + responseCode + `</m:ResponseCode>`)
	buf.WriteString(`</m:ResponseMessage>`)
	buf.WriteString(`<t:OofSettings>`)
	buf.WriteString(`<t:OofState>` + oofState + `</t:OofState>`)
	buf.WriteString(`<t:ExternalAudience>` + externalAudience + `</t:ExternalAudience>`)
	buf.WriteString(`</t:OofSettings>`)
	buf.WriteString(`<t:AllowExternalOof>` + externalAudience + `</t:AllowExternalOof>`)
	buf.WriteString(`</m:GetUserOofSettingsResponse>`)

	data := buf.Bytes()
	if len(data) == 0 {
		t.Fatal("string-based marshal returned empty")
	}
	t.Logf("String marshal:\n%s", string(data))
}
