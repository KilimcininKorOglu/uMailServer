package ews

import (
	"net/http"
	"testing"

	"github.com/umailserver/umailserver/internal/semcore"
)

// TestSearchDefFromFilter_Contains proves the text criteria of a restriction
// are extracted into the canonical definition, descending into And/Or branches.
func TestSearchDefFromFilter_Contains(t *testing.T) {
	filter := &SearchFilter{
		And: &SearchFilter{
			Contains: &ContainsFilter{
				FieldURI: &FieldURI{URI: "message:From"},
				Constant: ContainsConstType{Value: "boss@corp.com"},
			},
			Or: &SearchFilter{
				Contains: &ContainsFilter{
					FieldURI: &FieldURI{URI: "item:Subject"},
					Constant: ContainsConstType{Value: "invoice"},
				},
			},
		},
	}
	def := &semcore.SearchFolderDef{}
	searchDefFromFilter(filter, def)
	if def.From != "boss@corp.com" {
		t.Errorf("def.From = %q, want boss@corp.com", def.From)
	}
	if def.Subject != "invoice" {
		t.Errorf("def.Subject = %q, want invoice", def.Subject)
	}
}

// TestCreateFolder_SearchFolder proves a CreateFolder carrying a <t:SearchFolder>
// with a restriction and base folder set persists a folder identity marked with
// the parsed SearchFolderDef, so the search folder is created (not a plain folder).
func TestCreateFolder_SearchFolder(t *testing.T) {
	srv, cleanup := tmpEWSServer(t)
	defer cleanup()

	const email = "alice@example.com"
	if _, err := srv.identity.EnsureMailboxId(email); err != nil {
		t.Fatalf("EnsureMailboxId: %v", err)
	}

	body := `<CreateFolder xmlns="http://schemas.microsoft.com/exchange/services/2006/messages">
  <ParentFolderId><t:DistinguishedFolderId Id="searchfolders"/></ParentFolderId>
  <Folders>
    <t:SearchFolder>
      <t:DisplayName>From Boss</t:DisplayName>
      <t:SearchParameters Traversal="Deep">
        <t:Restriction>
          <t:And>
            <t:Contains ContainmentMode="Substring" ContainmentComparison="IgnoreCase">
              <t:FieldURI FieldURI="message:From"/>
              <t:Constant Value="boss@corp.com"/>
            </t:Contains>
            <t:IsGreaterThanOrEqualTo>
              <t:FieldURI FieldURI="message:DateTimeReceived"/>
              <t:FieldURIOrConstant><t:Constant Value="2026-01-01T00:00:00Z"/></t:FieldURIOrConstant>
            </t:IsGreaterThanOrEqualTo>
          </t:And>
        </t:Restriction>
        <t:BaseFolderIds>
          <t:DistinguishedFolderId Id="inbox"/>
        </t:BaseFolderIds>
      </t:SearchParameters>
    </t:SearchFolder>
  </Folders>
</CreateFolder>`

	rec := ewsRequest(t, srv, email, body)
	if rec.Code != http.StatusOK {
		t.Fatalf("status: %d, body: %s", rec.Code, rec.Body.String())
	}
	var resp CreateFolderResponse
	unmarshalFromBody(t, rec.Body.Bytes(), &resp)
	if len(resp.ResponseMessages.Messages) == 0 {
		t.Fatal("expected response messages")
	}
	msg := resp.ResponseMessages.Messages[0]
	if msg.ResponseClass != "Created" || msg.ResponseCode.Value != ErrNoError {
		t.Fatalf("CreateFolder failed: class=%s code=%s", msg.ResponseClass, msg.ResponseCode.Value)
	}
	if len(msg.Folders.Folders) == 0 || msg.Folders.Folders[0].FolderID.ID == "" {
		t.Fatal("expected non-empty FolderId for the created search folder")
	}

	folderID, err := semcore.NewFolderId(msg.Folders.Folders[0].FolderID.ID)
	if err != nil {
		t.Fatalf("parse folder id: %v", err)
	}
	folder, err := srv.identity.GetFolderByID(folderID)
	if err != nil {
		t.Fatalf("GetFolderByID: %v", err)
	}
	if folder.SearchDefinition == nil {
		t.Fatal("created folder is not a search folder (SearchDefinition is nil)")
	}
	def := folder.SearchDefinition
	if def.From != "boss@corp.com" {
		t.Errorf("def.From = %q, want boss@corp.com", def.From)
	}
	if def.DateFrom != "2026-01-01T00:00:00Z" {
		t.Errorf("def.DateFrom = %q, want 2026-01-01T00:00:00Z", def.DateFrom)
	}
	if def.Traversal != "Deep" {
		t.Errorf("def.Traversal = %q, want Deep", def.Traversal)
	}
	if len(def.BaseFolders) != 1 || def.BaseFolders[0] != "INBOX" {
		t.Errorf("def.BaseFolders = %v, want [INBOX]", def.BaseFolders)
	}
}
