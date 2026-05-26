# iRedMail Exchange Compatibility Gateway Tasarım ve Uygulama Planı

## 1. Amaç

Bu dokümanın amacı, **Outlook ile iRedMail arasında çalışan bir Exchange uyumluluk katmanı** tasarlamak ve uygulanabilir bir geliştirme planı çıkarmaktır.

Hedef şudur:

```text
Outlook
  |
  | Autodiscover + EWS
  | MAPI/HTTP + RPC/HTTP, ileri faz
  v
iRedMail Exchange Compatibility Gateway
  |
  +--> Dovecot / IMAP
  +--> Postfix / SMTP
  +--> SOGo / CalDAV
  +--> SOGo / CardDAV
  +--> Dovecot Pigeonhole / Sieve / ManageSieve
  +--> iRedMail SQL veya LDAP kullanıcı altyapısı
```

Bu proje iRedMail backend’ini değiştirmez. Amaç, önce Outlook’un EWS üzerinden beklediği temel davranışları, ileri fazlarda ise MAPI/HTTP ve RPC/HTTP bağlantı türlerinin ihtiyaç duyduğu Exchange/MAPI modelini iRedMail’in mevcut servislerine çevirmektir.

## 2. Kapsam

### 2.1 İlk hedef

İlk hedef, Outlook’un iRedMail hesabını EWS hesabı gibi tanıyabilmesi ve temel mail fonksiyonlarının çalışmasıdır. MAPI/HTTP ve RPC/HTTP, EWS MVP tamamlandıktan sonra ileri bağlantı türleri olarak ele alınır.

İlk sürümde hedeflenenler:

- Autodiscover
- EWS endpoint
- Basic authentication
- Klasör listeleme
- Mail listeleme
- Mail okuma
- Mail gönderme
- Mail silme
- Mail taşıma
- Okundu / okunmadı bilgisi
- Basit sync state
- Outlook request logger

### 2.2 İkinci hedef

İkinci aşamada Outlook tarafındaki daha ileri özelliklerin iRedMail servislerine bağlanması hedeflenir.

- Takvim senkronizasyonu
- Kişi senkronizasyonu
- Mail kuralları
- Out of office
- Free/busy
- Address book / ResolveNames
- Attachment desteği
- Flag / category / importance mapping

### 2.3 Üçüncü hedef

Daha sonra grup çalışması ve Exchange benzeri gelişmiş davranışlar eklenebilir.

- Shared mailbox
- Shared calendar
- Delegation benzeri davranışlar
- Global address list
- Public folder benzeri mapping
- Policy yönetimi
- Admin panel
- Migration/import araçları

## 3. Temel Mimari

Gateway, iRedMail’in mevcut bileşenlerini değiştirmeden, onların önünde bir uyumluluk katmanı olarak çalışır.

```text
+------------------+
| Outlook          |
| Desktop / Mac    |
+--------+---------+
         |
         | HTTPS
         | Autodiscover + EWS SOAP
         | MAPI/HTTP ve RPC/HTTP, ileri faz
         v
+-----------------------------------------+
| iRedMail Exchange Compatibility Gateway |
| Go Gateway Service                      |
+---------------------+-------------------+
                      |
                      +--> Exchange Compatibility Core
                      |       +--> Internal MAPI-like Store API
                      |       +--> PropertyBag / EntryId / SessionStore
                      |
                      +--> Auth Provider
                      |       +--> SQL
                      |       +--> LDAP
                      |       +--> Dovecot auth
                      |
                      +--> Mail Adapter
                      |       +--> Dovecot IMAP
                      |       +--> Postfix SMTP Submission
                      |
                      +--> Calendar Adapter
                      |       +--> SOGo CalDAV
                      |
                      +--> Contacts Adapter
                      |       +--> SOGo CardDAV
                      |
                      +--> Rules Adapter
                      |       +--> Dovecot Sieve
                      |       +--> ManageSieve
                      |
                      +--> State Store
                              +--> MariaDB/MySQL/PostgreSQL
```

### 3.1 Protokol Kapsam Kararları

Gateway’in client-facing protokol kapsamı aşamalı ilerlemelidir. İlk hedef, Outlook’un iRedMail hesabını Exchange benzeri şekilde tanıyabilmesi için Autodiscover ve EWS yüzeyini çalıştırmaktır. İleri hedefte RPC/HTTP, MAPI/HTTP ve Go içinde MAPI-like Store API eklenebilir; ancak bu destekler aynı backend servislerinin üstünde ayrı bir Exchange compatibility layer gerektirir.

#### 3.1.1 Desteklenecek client-facing protokoller

| Protokol     | Aşama     | Karar         | Gerekçe                                                                             |
|--------------|-----------|---------------|-------------------------------------------------------------------------------------|
| Autodiscover | MVP       | Desteklenecek | Outlook hesap kurulumu ve endpoint keşfi için gerekir.                              |
| EWS          | MVP       | Desteklenecek | Mail, folder, sync, calendar, contacts ve rules operasyonlarının ilk API yüzeyidir. |
| MAPI/HTTP    | İleri faz | Desteklenecek | Modern Outlook için Exchange mailbox deneyimine en yakın bağlantı türüdür.          |
| RPC/HTTP     | İleri faz | Desteklenecek | Eski Outlook Anywhere istemcileri için MAPI transport alternatifi sağlar.           |

İlk sürümde Outlook’a doğrudan sunulacak protokoller şunlardır:

```text
Autodiscover
EWS
```

İleri fazlarda eklenecek Outlook bağlantı türleri:

```text
MAPI/HTTP
RPC/HTTP
```

Backend tarafında ise mevcut iRedMail servisleri kullanılmaya devam eder:

```text
IMAP
SMTP submission
CalDAV
CardDAV
ManageSieve
SQL / LDAP / Dovecot auth
```

Bu kararın sınırı şudur: backend servisleri değişmez, fakat gateway’in içinde Exchange/MAPI veri modelini taklit eden kalıcı bir uyumluluk katmanı eklenir.

#### 3.1.2 Internal MAPI-like Store API kapsamı

Internal MAPI-like Store API, Outlook’un doğrudan bağlanacağı ayrı bir protokol olarak değil, gateway içindeki ortak mailbox API olarak tasarlanmalıdır. Amaç EWS, MAPI/HTTP ve RPC/HTTP handler’larının aynı mailbox modelini kullanmasıdır.

```text
EWS Operation
MAPI/HTTP Operation
RPC/HTTP MAPI Operation
  -> Internal MAPI-like Store API
  -> Backend adapters
```

Bu internal API şu kavramları sağlamalıdır:

```text
MailboxStore
FolderStore
MessageStore
PropertyBag
NamedPropertyStore
EntryIdMapper
ChangeTracker
SessionStore
NotificationStore
```

Internal MAPI-like Store API kapsamı şu şekilde tanımlanır:

| Bileşen                      | Karar            | Açıklama                                                                |
|------------------------------|------------------|-------------------------------------------------------------------------|
| Internal MAPI-like Store API | Desteklenecek    | EWS, MAPI/HTTP ve RPC/HTTP için ortak mailbox modelini sağlar.          |
| Client-facing MAPI protocol  | Desteklenmeyecek | Outlook bağlantısı MAPI/HTTP veya RPC/HTTP transport üzerinden yapılır. |

#### 3.1.3 Exchange compatibility layer zorunluluğu

MAPI/HTTP ve RPC/HTTP eklenirse IMAP/SMTP/CalDAV/CardDAV adapter yaklaşımı tek başına yeterli olmaz. Gateway içinde şu Exchange benzeri katmanlar gerekir:

```text
Mailbox table
Folder hierarchy table
Folder contents table
Associated contents table
Recipient table
Attachment table
MAPI property bag
Named properties
EntryId modeli
Change number / change tracking
Stateful session yönetimi
Notification / subscription modeli
Address book / NSPI benzeri model
```

Bu katmanlar iRedMail backend’ini değiştirmez. Yalnızca Outlook’a gösterilen mailbox modelini Exchange/MAPI semantiğine yaklaştırır.

## 4. Endpoint Tasarımı

Gateway MVP aşamasında Autodiscover ve EWS endpoint’lerini sağlar. İleri fazlarda MAPI/HTTP ve RPC/HTTP endpoint’leri eklenir.

MVP endpoint’leri:

```text
/autodiscover/autodiscover.xml
/Autodiscover/Autodiscover.xml
/EWS/Exchange.asmx
```

İleri faz endpoint’leri:

```text
/mapi/emsmdb
/mapi/nspi
/rpc/rpcproxy.dll
```

`/mapi/emsmdb` mailbox erişimi, `/mapi/nspi` address book benzeri işlemler, `/rpc/rpcproxy.dll` ise Outlook Anywhere uyumluluğu için tasarlanır.

### 4.1 Autodiscover endpoint

Outlook hesap kurulumu sırasında önce Autodiscover çağırır. Gateway, Outlook’a EWS endpoint adresini döner.

Örnek mantıksal cevap:

```xml
<Autodiscover>
  <Response>
    <Account>
      <Protocol>
        <Type>EXCH</Type>
        <EwsUrl>https://mail.example.com/EWS/Exchange.asmx</EwsUrl>
        <ASUrl>https://mail.example.com/EWS/Exchange.asmx</ASUrl>
      </Protocol>
    </Account>
  </Response>
</Autodiscover>
```

#### 4.1.1 Autodiscover kapsamı

İlk sürümde Autodiscover desteği özellikle Outlook hesap kurulumu için gereken en küçük davranışa odaklanır.

Desteklenecek ilk akış:

```text
POX Autodiscover request
  -> kullanıcının e-posta adresini oku
  -> domain için gateway base_url değerini belirle
  -> EWS endpoint URL döndür
```

İlk sürüm kararları:

- `/autodiscover/autodiscover.xml` ve `/Autodiscover/Autodiscover.xml` aynı controller’a yönlenir.
- Basic authentication destekleyen Outlook istemcileri hedeflenir.
- EWS URL config üzerinden üretilir; request host değeri tek başına güvenilir kaynak kabul edilmez.
- Autodiscover response namespace ve schema uyumluluğu fixture testleriyle sabitlenir.
- Redirect, OAuth ve çok tenantlı domain discovery ilk MVP kapsamına alınmaz.

Autodiscover fixture hedefleri:

```text
tests/fixtures/requests/Autodiscover-basic.xml
tests/fixtures/responses/Autodiscover-basic.xml
tests/fixtures/responses/Autodiscover-invalid-credentials.xml
```

### 4.2 EWS endpoint

Outlook, EWS SOAP isteklerini bu adrese gönderir.

```text
/EWS/Exchange.asmx
```

Bu endpoint gelen SOAP operasyonunu tespit eder, ilgili handler’a yönlendirir ve EWS uyumlu SOAP response döner.

#### 4.2.1 EWS SOAP uyumluluk kuralları

EWS endpoint yalnızca operation name tespit etmekle yetinmemelidir. Outlook request’lerinde response şeklini belirleyen alanlar da parse edilmelidir.

İlk parser kapsamı:

```text
SOAP Envelope
SOAP Body
Operation name
RequestServerVersion
FolderShape / ItemShape
BaseShape
AdditionalProperties
ParentFolderIds
IndexedPageItemView
SyncState
MaxChangesReturned
```

Response builder her response için şu ortak alanları doğru üretmelidir:

```text
SOAP Envelope namespace
messages namespace
types namespace
ResponseClass
ResponseCode
MessageText, varsa
Operation-specific response message
```

Desteklenmeyen operasyon politikası:

```text
Known but unsupported operation
  -> operation-specific ErrorInvalidOperation response

Unknown SOAP operation
  -> EWS uyumlu fault response

Parse edilemeyen XML
  -> ErrorInvalidRequest veya SOAP fault
```

Boş başarılı response yalnızca Outlook’un açıkça tolere ettiği fixture ile kanıtlanmış operasyonlarda kullanılmalıdır.

#### 4.2.2 EWS operasyon uyumluluk matrisi

Her operasyon için desteklenen request alanları, yok sayılan alanlar ve response alanları test fixture’larıyla belgelenmelidir.

| Operasyon          | İlk desteklenen request alanları                | İlk response alanları                                      | Backend                 | Fixture hedefi |
|--------------------|-------------------------------------------------|------------------------------------------------------------|-------------------------|----------------|
| GetServerTimeZones | RequestServerVersion                            | TimeZoneDefinition                                         | Config / Go timezone    | Var            |
| GetFolder          | FolderShape, DistinguishedFolderId, FolderId    | FolderId, ChangeKey, DisplayName, TotalCount, UnreadCount  | IMAP STATUS / state     | Var            |
| FindFolder         | FolderShape, ParentFolderIds                    | RootFolder, Folder list                                    | IMAP LIST / SPECIAL-USE | Var            |
| FindItem           | ItemShape, ParentFolderIds, IndexedPageItemView | ItemId, ChangeKey, Subject, From, DateTimeReceived, IsRead | IMAP SEARCH / FETCH     | Var            |
| GetItem            | ItemShape, ItemIds, AdditionalProperties        | Message body, headers, attachment metadata                 | IMAP FETCH / MIME       | Var            |
| SyncFolderItems    | SyncState, MaxChangesReturned, ItemShape        | Create, Update, Delete, new SyncState                      | IMAP UID / MODSEQ       | Var            |
| CreateItem         | MessageDisposition, SavedItemFolderId, Items    | ItemId, ChangeKey veya send result                         | SMTP / IMAP APPEND      | Var            |
| SendItem           | ItemId, SavedItemFolderId                       | Success veya EWS error                                     | SMTP / IMAP APPEND      | Var            |
| DeleteItem         | ItemIds, DeleteType                             | Success veya item error                                    | IMAP MOVE / STORE       | Var            |
| MoveItem           | ItemIds, ToFolderId                             | New ItemId / ChangeKey                                     | IMAP MOVE               | Var            |
| UpdateItem         | ItemChanges                                     | Updated ItemId / ChangeKey                                 | IMAP flags              | Var            |

Bu tablo geliştirme sırasında genişletilmeli; fixture ile doğrulanmayan alanlar desteklenmiş kabul edilmemelidir.

### 4.3 MAPI/HTTP endpoint

MAPI/HTTP modern Outlook istemcilerinin Exchange mailbox erişimi için kullandığı stateful bağlantı modelidir. Bu endpoint EWS handler’ları gibi doğrudan IMAP adapter’ına gitmemelidir; önce internal MAPI-like Store API’ye bağlanmalıdır.

Temel akış:

```text
Outlook MAPI/HTTP
  -> /mapi/emsmdb veya /mapi/nspi
  -> MAPI/HTTP request parser
  -> SessionStore
  -> Internal MAPI-like Store API
  -> Backend adapters
```

İlk MAPI/HTTP PoC kapsamı:

```text
1. Autodiscover MAPI endpoint bilgisini döndürür.
2. Kullanıcı authenticate edilir.
3. MAPI session oluşturulur.
4. Mailbox açılır.
5. Folder hierarchy table döner.
6. Inbox contents table döner.
7. Bir mesajın property bag değeri okunur.
```

İlk PoC kapsamına calendar, contacts, rules, delegation ve shared mailbox dahil edilmez.

### 4.4 RPC/HTTP endpoint

RPC/HTTP, Outlook Anywhere uyumluluğu için MAPI RPC operasyonlarını HTTP üzerinden taşır. Bu destek MAPI/HTTP’den sonra ele alınmalıdır; çünkü RPC/HTTP transport katmanının arkasında yine MAPI semantiği gerekir.

Temel akış:

```text
Outlook RPC/HTTP
  -> /rpc/rpcproxy.dll
  -> RPC over HTTP transport parser
  -> MAPI RPC operation mapper
  -> SessionStore
  -> Internal MAPI-like Store API
  -> Backend adapters
```

RPC/HTTP için gerekli ek parçalar:

```text
RPC context handle yönetimi
Connection state takibi
Request multiplexing
MAPI RPC operation mapping
Outlook Anywhere Autodiscover bilgisi
```

RPC/HTTP desteği EWS MVP ve MAPI/HTTP PoC tamamlanmadan uygulanmamalıdır.

### 4.5 Advanced Autodiscover response

MAPI/HTTP ve RPC/HTTP eklendiğinde Autodiscover yalnızca EWS URL döndürmez. Outlook profilini oluşturabilmek için bağlantı türlerini de bildirmelidir.

Advanced Autodiscover response şu bilgileri üretmelidir:

```text
EWS URL
MAPI/HTTP EMSMDB endpoint
MAPI/HTTP NSPI endpoint
RPC/HTTP proxy endpoint
Authentication methods
Server version bilgisi
Address book endpoint bilgisi
```

Bu bilgiler config üzerinden üretilmeli; request host değeri tek başına güvenilir kaynak kabul edilmemelidir.


## 5. Backend Mapping

| EWS tarafı             | iRedMail tarafı                         |
|------------------------|-----------------------------------------|
| Authentication         | Dovecot auth / SQL / LDAP               |
| Inbox                  | Dovecot IMAP `INBOX`                    |
| Sent Items             | Dovecot IMAP `Sent`                     |
| Drafts                 | Dovecot IMAP `Drafts`                   |
| Deleted Items          | Dovecot IMAP `Trash`                    |
| Junk Email             | Dovecot IMAP `Junk`                     |
| FindFolder / GetFolder | IMAP `LIST`, `STATUS`                   |
| FindItem               | IMAP `SEARCH`, `SORT`, `FETCH`          |
| GetItem                | IMAP `FETCH BODY`, `BODYSTRUCTURE`      |
| CreateItem draft       | IMAP `APPEND Drafts`                    |
| SendItem               | SMTP submission                         |
| DeleteItem             | IMAP `MOVE Trash` veya `STORE \Deleted` |
| MoveItem               | IMAP `MOVE`                             |
| UpdateItem read/unread | IMAP flags                              |
| Attachments            | IMAP MIME parser                        |
| Calendar               | SOGo CalDAV                             |
| Contacts               | SOGo CardDAV                            |
| Inbox Rules            | Dovecot Sieve / ManageSieve             |
| Out of Office          | Sieve vacation                          |
| ResolveNames           | LDAP / SQL address book                 |
| FreeBusy               | SOGo CalDAV freebusy                    |


### 5.1 MAPI model mapping

MAPI/HTTP ve RPC/HTTP desteğinde backend servisleri aynı kalır, ancak veriler MAPI property modeline çevrilir.

| MAPI tarafı            | iRedMail backend karşılığı                       |
|------------------------|--------------------------------------------------|
| Mailbox                | Kullanıcı mailbox kimliği / auth user            |
| EntryId                | Gateway opaque id + backend object map           |
| Folder hierarchy table | IMAP LIST / SPECIAL-USE / config folder mapping  |
| Folder contents table  | IMAP SEARCH / FETCH / UID / FLAGS                |
| Message property bag   | MIME headers, MIME body, IMAP flags, gateway map |
| Recipient table        | MIME To/Cc/Bcc headers                           |
| Attachment table       | MIME parts                                       |
| Appointment item       | SOGo CalDAV VEVENT                               |
| Contact item           | SOGo CardDAV VCARD                               |
| Rules table            | Dovecot Sieve / gateway rule map                 |
| Address book / NSPI    | SQL / LDAP / CardDAV contacts                    |
| Notifications          | IMAP IDLE / polling / gateway event store        |

MAPI mapping, EWS mapping’den daha geniştir. Bu yüzden MAPI tarafı doğrudan backend adapter’larına değil, internal MAPI-like Store API’ye bağlanmalıdır.


### 5.1 IMAP capability detection

Gateway kullanıcı mailbox’ına bağlandıktan sonra IMAP capability bilgisini okumalı ve davranışını buna göre seçmelidir.

Kontrol edilecek capability değerleri:

```text
MOVE
UIDPLUS
CONDSTORE
QRESYNC
SORT
SPECIAL-USE
```

Davranış kararları:

| Capability  | Varsa                                             | Yoksa                                           |
|-------------|---------------------------------------------------|-------------------------------------------------|
| MOVE        | `UID MOVE` kullanılır                             | `COPY + STORE \Deleted + EXPUNGE` fallback      |
| UIDPLUS     | APPEND/COPY sonrası yeni UID güvenilir alınır     | Yeni UID eşlemesi header/message-id ile aranır  |
| CONDSTORE   | MODSEQ tabanlı ChangeKey üretilebilir             | ChangeKey flags ve UID state üzerinden üretilir |
| QRESYNC     | Daha verimli değişiklik takibi yapılabilir        | Snapshot tabanlı sync kullanılır                |
| SORT        | IMAP server-side sort kullanılabilir              | Uygulama tarafında sıralama yapılır             |
| SPECIAL-USE | Sent/Drafts/Trash/Junk otomatik tespit edilebilir | Config folder mapping kullanılır                |

Capability sonucu request bazlı değil, kullanıcı oturumu veya kısa süreli cache içinde tutulabilir.

## 6. Go Proje Yapısı

Önerilen Go servis klasör yapısı:

```text
iredmail-exchange-gateway/
  cmd/
    gateway/
      main.go
    migrate/
      main.go

  config/
    config.example.yaml
    logging.example.yaml

  internal/
    httpserver/
      router.go
      request.go
      response.go
      middleware/
        basic_auth.go
        request_logger.go

    autodiscover/
      controller.go
      response_builder.go

    ews/
      controller.go
      soap_request_parser.go
      soap_response_builder.go
      fault_builder.go
      operations/
        get_server_time_zones.go
        get_folder.go
        find_folder.go
        find_item.go
        get_item.go
        sync_folder_items.go
        create_item.go
        send_item.go
        delete_item.go
        move_item.go
        update_item.go
        create_attachment.go
        get_attachment.go
        delete_attachment.go
        resolve_names.go
        get_inbox_rules.go
        update_inbox_rules.go
        get_user_availability.go
      types/
        folder_id.go
        item_id.go
        change_key.go
        distinguished_folder_id.go
        message.go
        calendar_item.go
        contact.go
        rule.go

    mapi/
      http/
        emsmdb_handler.go
        nspi_handler.go
        request_parser.go
        response_writer.go
      rpc/
        rpc_http_handler.go
        context_handle_store.go
        operation_mapper.go
      store/
        mailbox_store.go
        folder_store.go
        message_store.go
        property_bag.go
        named_property_store.go
        entry_id_mapper.go
        change_tracker.go
        session_store.go
        notification_store.go
      types/
        entry_id.go
        property_tag.go
        property_value.go
        folder_row.go
        contents_row.go

    backend/
      auth/
        provider.go
        iredmail_sql.go
        iredmail_ldap.go
        dovecot.go
      mail/
        mailbox.go
        imap_mailbox.go
        smtp_sender.go
        mime_parser.go
        mime_builder.go
        folder_mapper.go
        imap_message_to_ews.go
        imap_message_to_mapi.go
      calendar/
        calendar.go
        caldav_calendar.go
        sogo_calendar_adapter.go
        ics_to_ews_calendar_item.go
        ews_calendar_item_to_ics.go
        ics_to_mapi_appointment.go
      contacts/
        contacts.go
        carddav_contacts.go
        sogo_contacts_adapter.go
        vcard_to_ews_contact.go
        ews_contact_to_vcard.go
        vcard_to_mapi_contact.go
      rules/
        rules.go
        sieve_rule_adapter.go
        managesieve_client.go
        sieve_parser.go
        sieve_generator.go

    state/
      store.go
      user_map_store.go
      folder_map_store.go
      item_map_store.go
      sync_state_store.go
      mapi_session_store.go
      mapi_entry_store.go
      mapi_property_store.go

    logging/
      ews_request_logger.go
      ews_response_logger.go
      protocol_logger.go

  database/
    migrations/
      001_create_ews_folder_map.sql
      002_create_ews_item_map.sql
      003_create_ews_sync_state.sql
      004_create_ews_device_map.sql
      005_create_ews_rule_map.sql
      006_create_mapi_sessions.sql
      007_create_mapi_entry_map.sql
      008_create_mapi_named_properties.sql
      009_create_mapi_property_cache.sql

  tests/
    fixtures/
      requests/
      responses/
    unit/
    integration/

  go.mod
  go.sum
```

Go servis tek binary olarak çalışmalıdır. Protokol motoru `net/http`, `context.Context`, goroutine tabanlı worker’lar ve graceful shutdown üzerine kurulmalıdır.


## 7. EWS Controller Akışı

Temel controller yapısı:

```go
type EwsController struct {
    auth       AuthProvider
    soapParser SoapRequestParser
    operations map[string]EwsOperation
    faults     FaultBuilder
}

func (c *EwsController) Handle(ctx context.Context, r *http.Request) (*Response, error) {
    user, err := c.auth.Authenticate(ctx, r)
    if err != nil {
        return c.faults.InvalidCredentials(), nil
    }

    soap, err := c.soapParser.Parse(r.Body)
    if err != nil {
        return c.faults.InvalidRequest(), nil
    }

    operation, ok := c.operations[soap.OperationName]
    if !ok {
        return c.faults.Unsupported(soap.OperationName), nil
    }

    return operation.Handle(ctx, user, soap)
}
```

Operation registry deterministic olmalıdır; operation routing reflection veya dinamik string çağrılarıyla yapılmamalıdır.


## 8. İlk Desteklenecek EWS Operasyonları

### 8.1 Faz 1: Hesap kurulumu ve klasör ağacı

İlk desteklenecek operasyonlar:

```text
Autodiscover
GetServerTimeZones
GetFolder
FindFolder
```

Amaç:

- Outlook hesabı tanısın.
- Root folder bilgisi dönebilsin.
- Inbox, Sent, Drafts, Trash gibi klasörler listelensin.
- Distinguished folder id mapping çalışsın.

### 8.2 Faz 2: Mail listeleme ve okuma

```text
FindItem
GetItem
SyncFolderItems
```

Amaç:

- Outlook mailbox içeriğini listeleyebilsin.
- Mesaj başlıkları ve preview alınabilsin.
- Mesaj detayları, body ve attachment bilgileri okunabilsin.
- Temel sync state tutulabilsin.

### 8.3 Faz 3: Mail gönderme

```text
CreateItem
SendItem
CreateAttachment
GetAttachment
DeleteAttachment
```

Amaç:

- Outlook’tan mail gönderilebilsin.
- Draft oluşturulabilsin.
- Attachment eklenebilsin.
- Gönderilen mail Sent klasörüne kaydedilsin.

### 8.4 Faz 4: Silme, taşıma, flag

```text
DeleteItem
MoveItem
UpdateItem
```

Amaç:

- Mail silme.
- Mail taşıma.
- Okundu / okunmadı bilgisi.
- Flag / importance mapping.

### 8.5 Faz 5: Rules

```text
GetInboxRules
UpdateInboxRules
```

Amaç:

- Outlook rule ekranında mevcut kurallar gösterilebilsin.
- Kullanıcının Outlook’tan oluşturduğu desteklenen kurallar Sieve script’e çevrilsin.
- Sieve script ManageSieve üzerinden kullanıcı hesabına kaydedilsin.

## 9. Folder Mapping

Outlook’un distinguished folder id değerleri IMAP klasörlerine çevrilir.

```go
type ImapFolderMapper struct {
    folders map[string]string
}

func (m ImapFolderMapper) DistinguishedToIMAP(id string) (string, error) {
    folder, ok := m.folders[id]
    if !ok {
        return "", fmt.Errorf("unknown folder: %s", id)
    }

    return folder, nil
}

func (m ImapFolderMapper) IMAPToDistinguished(folder string) (string, bool) {
    for distinguished, imapFolder := range m.folders {
        if imapFolder == folder {
            return distinguished, true
        }
    }

    return "", false
}
```


Klasör isimleri iRedMail kurulumuna göre değişebileceği için config ile override desteklenmelidir.

Örnek config:

```yaml
folders:
  inbox: INBOX
  sentitems: Sent
  drafts: Drafts
  deleteditems: Trash
  junkemail: Junk
  outbox: Outbox
```


## 10. ItemId, FolderId ve ChangeKey Modeli

Outlook EWS tarafında öğeleri şu kavramlarla takip eder:

```text
FolderId
ItemId
ChangeKey
SyncState
```

iRedMail backend’lerinde bu kavramların birebir karşılığı yoktur. Bu yüzden gateway kendi ID ve state katmanını sağlar.

### 10.1 FolderId

Her IMAP, CalDAV ve CardDAV klasörü için bir EWS folder id üretilir.

Örnek:

```text
ewsFolderId = opaque id
backend_type = imap
backend_path = INBOX
```

### 10.2 ItemId

Her mail, takvim öğesi ve kişi için bir EWS item id üretilir.

IMAP mesajı için backend kimliği:

```json
{
  "type": "imap",
  "folder": "INBOX",
  "uidvalidity": 123456,
  "uid": 9981,
  "modseq": 887766
}
```

CalDAV item için backend kimliği:

```json
{
  "type": "caldav",
  "url": "https://mail.example.com/SOGo/dav/user@example.com/Calendar/personal/event-123.ics",
  "etag": "\"abc123\""
}
```

CardDAV item için backend kimliği:

```json
{
  "type": "carddav",
  "url": "https://mail.example.com/SOGo/dav/user@example.com/Contacts/personal/contact-123.vcf",
  "etag": "\"def456\""
}
```

### 10.3 ChangeKey

ChangeKey backend state’ten türetilir.

IMAP için:

```text
changeKey = hash(uidvalidity + uid + modseq + flags)
```

CalDAV/CardDAV için:

```text
changeKey = hash(url + etag)
```

### 10.4 SyncFolderItems algoritması

`SyncFolderItems`, Outlook’un aynı klasörü tekrar tekrar tam listelemeden takip edebilmesi için deterministik çalışmalıdır.

İlk sync akışı:

```text
Request SyncState boş
  -> IMAP folder seç
  -> UIDVALIDITY oku
  -> mevcut UID listesini ve flags değerlerini al
  -> her mesaj için EWS ItemId / ChangeKey üret veya mevcut map’i bul
  -> Create değişiklikleri döndür
  -> yeni SyncState üret
  -> snapshot kaydet
```

Devam sync akışı:

```text
Request SyncState var
  -> SyncState kullanıcının klasörüne ait mi kontrol et
  -> önceki snapshot’ı yükle
  -> güncel UID / flags / modseq bilgisini al
  -> yeni UID değerlerini Create olarak döndür
  -> flags veya modseq değişenleri Update olarak döndür
  -> önceki snapshot’ta olup güncel listede olmayanları Delete olarak döndür
  -> yeni SyncState üret
  -> snapshot’ı güncelle
```

Dikkat edilmesi gereken kurallar:

- `SyncState` kullanıcı ve klasör sınırına bağlı olmalıdır.
- Aynı `SyncState` tekrar gönderildiğinde response tutarlı olmalıdır.
- `MaxChangesReturned` desteklenmeli ve response parçalı dönebilmeli.
- `UIDVALIDITY` değişirse eski snapshot geçersiz kabul edilmeli ve Outlook’a yeniden sync başlatmasını sağlayacak EWS hata cevabı dönülmelidir.
- `CONDSTORE` yoksa MODSEQ zorunlu kabul edilmemeli; snapshot karşılaştırması kullanılmalıdır.
- Silinen veya expunge edilen mesajlar Delete değişikliği olarak raporlanmalıdır.

Sync state içeriği opaque olmalı, ancak backend tarafında şu veriler izlenmelidir:

```json
{
  "folder_id": "...",
  "uidvalidity": 123456,
  "last_seen_uid": 9981,
  "modseq": 887766,
  "snapshot_version": 12
}
```

Snapshot sadece `last_seen_uid` ile sınırlı kalmamalıdır; silme ve flag değişikliklerini yakalamak için folder bazlı UID -> ChangeKey kaydı tutulmalıdır.

## 11. Veritabanı Modeli

### 11.1 Advanced MAPI state tabloları

MAPI/HTTP ve RPC/HTTP desteği için EWS tablolarına ek olarak daha geniş state tabloları gerekir.

```sql
CREATE TABLE mapi_sessions (
    id BIGINT PRIMARY KEY AUTO_INCREMENT,
    user_id VARCHAR(255) NOT NULL,
    session_id VARCHAR(255) NOT NULL,
    client_version VARCHAR(128),
    state_blob JSON NOT NULL,
    created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
    last_seen_at TIMESTAMP NULL,
    UNIQUE KEY uniq_mapi_session (user_id, session_id)
);
```

```sql
CREATE TABLE mapi_entry_map (
    id BIGINT PRIMARY KEY AUTO_INCREMENT,
    user_id VARCHAR(255) NOT NULL,
    entry_id VARCHAR(512) NOT NULL,
    object_type VARCHAR(64) NOT NULL,
    backend_type VARCHAR(32) NOT NULL,
    backend_id TEXT NOT NULL,
    parent_entry_id VARCHAR(512),
    change_number BIGINT NOT NULL DEFAULT 0,
    created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
    updated_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
    UNIQUE KEY uniq_mapi_entry (user_id, entry_id)
);
```

```sql
CREATE TABLE mapi_named_properties (
    id BIGINT PRIMARY KEY AUTO_INCREMENT,
    user_id VARCHAR(255) NOT NULL,
    property_guid VARCHAR(64) NOT NULL,
    property_name VARCHAR(255) NOT NULL,
    property_id INT NOT NULL,
    created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
    UNIQUE KEY uniq_named_property (user_id, property_guid, property_name)
);
```

```sql
CREATE TABLE mapi_property_cache (
    id BIGINT PRIMARY KEY AUTO_INCREMENT,
    user_id VARCHAR(255) NOT NULL,
    entry_id VARCHAR(512) NOT NULL,
    property_tag VARCHAR(64) NOT NULL,
    property_value JSON NOT NULL,
    updated_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
    UNIQUE KEY uniq_property_cache (user_id, entry_id, property_tag)
);
```

Bu tablolar backend verisinin kaynağı değildir. Kaynak yine Dovecot, Postfix, SOGo, Sieve ve auth backend’leridir. Tablolar Outlook’a gösterilen MAPI kimliklerini, state bilgisini ve property cache değerlerini tutar.

### 11.1 Folder map

```sql
CREATE TABLE ews_folder_map (
    id BIGINT PRIMARY KEY AUTO_INCREMENT,
    user_id VARCHAR(255) NOT NULL,
    ews_folder_id VARCHAR(512) NOT NULL,
    distinguished_id VARCHAR(128),
    backend_type VARCHAR(32) NOT NULL,
    backend_path TEXT NOT NULL,
    parent_ews_folder_id VARCHAR(512),
    change_key VARCHAR(255),
    created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
    updated_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
    UNIQUE KEY uniq_folder (user_id, ews_folder_id)
);
```

### 11.2 Item map

```sql
CREATE TABLE ews_item_map (
    id BIGINT PRIMARY KEY AUTO_INCREMENT,
    user_id VARCHAR(255) NOT NULL,
    ews_item_id VARCHAR(512) NOT NULL,
    folder_id VARCHAR(512) NOT NULL,
    backend_type VARCHAR(32) NOT NULL,
    backend_id TEXT NOT NULL,
    backend_folder VARCHAR(512),
    backend_uidvalidity BIGINT,
    backend_uid BIGINT,
    backend_modseq BIGINT NULL,
    change_key VARCHAR(255),
    is_deleted TINYINT(1) NOT NULL DEFAULT 0,
    last_seen_at TIMESTAMP NULL,
    created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
    updated_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
    UNIQUE KEY uniq_item (user_id, ews_item_id),
    KEY idx_backend_uid (user_id, backend_type, backend_folder, backend_uidvalidity, backend_uid)
);
```

### 11.3 Sync state

```sql
CREATE TABLE ews_sync_state (
    id BIGINT PRIMARY KEY AUTO_INCREMENT,
    user_id VARCHAR(255) NOT NULL,
    folder_id VARCHAR(512) NOT NULL,
    sync_state VARCHAR(512) NOT NULL,
    backend_modseq BIGINT,
    last_seen_uid BIGINT,
    created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
    updated_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
    UNIQUE KEY uniq_sync_state (user_id, folder_id, sync_state)
);
```

### 11.4 Device map

```sql
CREATE TABLE ews_device_map (
    id BIGINT PRIMARY KEY AUTO_INCREMENT,
    user_id VARCHAR(255) NOT NULL,
    device_id VARCHAR(255),
    user_agent TEXT,
    client_ip VARCHAR(64),
    last_seen_at TIMESTAMP NULL,
    created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
    UNIQUE KEY uniq_device (user_id, device_id)
);
```

### 11.5 Rule map

```sql
CREATE TABLE ews_rule_map (
    id BIGINT PRIMARY KEY AUTO_INCREMENT,
    user_id VARCHAR(255) NOT NULL,
    ews_rule_id VARCHAR(512) NOT NULL,
    sieve_rule_name VARCHAR(255),
    priority INT NOT NULL DEFAULT 0,
    enabled TINYINT(1) NOT NULL DEFAULT 1,
    rule_json JSON NOT NULL,
    created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
    updated_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
    UNIQUE KEY uniq_rule (user_id, ews_rule_id)
);
```

## 12. Mail Dönüşümü

IMAP’ten gelen mesaj EWS message formatına çevrilir.

```go
func (c ImapMessageToEwsMessage) Convert(message ImapMessage) EwsMessage {
    return EwsMessage{
        ItemID:           c.ids.ItemID(message),
        ChangeKey:        c.ids.ChangeKey(message),
        Subject:          message.Subject,
        From:             message.From,
        To:               message.To,
        Cc:               message.Cc,
        DateTimeReceived: message.Date,
        IsRead:           message.HasFlag(`\\Seen`),
        HasAttachments:   message.HasAttachments(),
        Size:             message.Size,
        BodyPreview:      message.Preview(),
    }
}
```

### 12.1 Attachment metadata

Attachment içerik indirme ayrı fazda geliştirilebilir; ancak attachment’lı mesajların Outlook’ta doğru görünmesi için MVP içinde temel metadata döndürülmelidir.

MVP davranışı:

```text
GetItem
  -> HasAttachments değerini doğru döndür
  -> AttachmentId, Name, ContentType, Size metadata üret
  -> Attachment content gerekiyorsa desteklenmeyen içerik için EWS uyumlu hata döndür
```

Attachment içeriği loglanmamalıdır. Büyük attachment’lar için sonraki fazda streaming kullanılmalıdır.

## 13. Mail Gönderme

EWS `CreateItem` ve `SendItem`, SMTP submission’a çevrilir.

### 13.1 Send and save copy

```text
EWS CreateItem MessageDisposition=SendAndSaveCopy
  -> MIME oluştur
  -> SMTP ile gönder
  -> IMAP Sent klasörüne APPEND
```

### 13.2 Draft oluşturma

```text
EWS CreateItem MessageDisposition=SaveOnly
  -> MIME oluştur
  -> IMAP Drafts klasörüne APPEND
  -> EWS ItemId döndür
```

### 13.3 Reply / forward

Reply ve forward için:

```text
EWS CreateItem / SendItem
  -> In-Reply-To / References header üret
  -> MIME oluştur
  -> SMTP ile gönder
  -> Sent klasörüne kaydet
```

## 14. Delete, Move ve Update Mapping

### 14.1 DeleteItem

```text
DeleteItem DeleteType=MoveToDeletedItems
  -> IMAP MOVE Trash

DeleteItem DeleteType=HardDelete
  -> IMAP STORE +FLAGS \Deleted
  -> IMAP EXPUNGE

DeleteItem DeleteType=SoftDelete
  -> config'e göre Trash veya \Deleted
```

### 14.2 MoveItem

```text
MoveItem
  -> IMAP MOVE source target
  -> yeni UID al
  -> ews_item_map güncelle
  -> yeni ItemId veya yeni ChangeKey döndür
```

### 14.3 UpdateItem

```text
UpdateItem IsRead=true
  -> IMAP STORE +FLAGS \Seen

UpdateItem IsRead=false
  -> IMAP STORE -FLAGS \Seen

UpdateItem Importance
  -> IMAP keyword veya header mapping

UpdateItem Categories
  -> IMAP keywords veya gateway metadata
```

## 15. Rules: EWS -> Sieve

Outlook rules desteği için EWS operasyonları Sieve backend’e map edilir.

```text
GetInboxRules
  -> mevcut Sieve script parse edilir
  -> desteklenen kurallar EWS Rule formatına çevrilir

UpdateInboxRules
  -> Outlook’tan gelen rule operasyonları alınır
  -> gateway rule modeline çevrilir
  -> Sieve script üretilir
  -> ManageSieve ile upload edilir
```

### 15.1 Rule modeli

```go
type MailRule struct {
    ID                string
    DisplayName       string
    Enabled           bool
    Priority          int
    FromContains      string
    SubjectContains   string
    BodyContains      string
    RecipientContains string
    MoveToFolder      string
    CopyToFolder      string
    Delete            bool
    MarkAsRead        bool
    StopProcessing    bool
}
```


### 15.2 Sieve generator

```go
type SieveGenerator struct {
    encoder SieveEncoder
}

func (g SieveGenerator) Generate(rules []MailRule) (string, error) {
    script := NewSieveScript([]string{"fileinto", "imap4flags", "vacation"})

    for _, rule := range rules {
        if !rule.Enabled {
            continue
        }

        block, err := g.encoder.RuleBlock(rule)
        if err != nil {
            return "", err
        }

        if block != nil {
            script.AddBlock(block)
        }
    }

    return script.String(), nil
}
```


### 15.3 Örnek rule mapping

Outlook rule:

```text
From contains boss@example.com
Move to folder Important
```

Sieve karşılığı:

```sieve
require ["fileinto"];

if address :contains "From" "boss@example.com" {
    fileinto "Important";
    stop;
}
```

Outlook rule:

```text
Subject contains invoice
Move to folder Invoices
```

Sieve karşılığı:

```sieve
require ["fileinto"];

if header :contains "Subject" "invoice" {
    fileinto "Invoices";
    stop;
}
```

### 15.4 Sieve üretim güvenliği

Bu bölümdeki generator örneği yalnızca mantığı göstermek içindir. Üretim kodu doğrudan string concatenation ve `addslashes` ile Sieve script üretmemelidir.

Üretim davranışı:

```text
Rule model
  -> Sieve AST veya güvenli command modeli
  -> Sieve string encoder
  -> ManageSieve upload
```

Encoder şu değerleri doğru escape etmelidir:

- Header değerleri
- E-posta adresleri
- Klasör isimleri
- Body koşulları
- Unicode karakterler
- Çift tırnak, ters slash ve satır sonları

Desteklenmeyen rule koşulları sessizce düşürülmemeli; response içinde desteklenmediği açıkça belirtilmelidir.

## 16. Calendar Mapping

Outlook EWS calendar item’ları SOGo CalDAV item’larına map edilir.

| EWS               | CalDAV / iCalendar |
|-------------------|--------------------|
| CalendarItem      | VEVENT             |
| Subject           | SUMMARY            |
| Body              | DESCRIPTION        |
| Location          | LOCATION           |
| Start             | DTSTART            |
| End               | DTEND              |
| RequiredAttendees | ATTENDEE           |
| Organizer         | ORGANIZER          |
| Recurrence        | RRULE              |
| Reminder          | VALARM             |
| UID               | UID                |
| ChangeKey         | ETag               |

İlk calendar scope:

```text
FindItem calendar folder
GetItem calendar item
CreateItem calendar item
UpdateItem calendar item
DeleteItem calendar item
SyncFolderItems calendar
```

## 17. Contacts Mapping

Outlook EWS contacts SOGo CardDAV item’larına map edilir.

| EWS               | vCard |
|-------------------|-------|
| Contact           | VCARD |
| GivenName         | N     |
| Surname           | N     |
| DisplayName       | FN    |
| EmailAddresses    | EMAIL |
| PhoneNumbers      | TEL   |
| PhysicalAddresses | ADR   |
| CompanyName       | ORG   |
| JobTitle          | TITLE |
| Notes             | NOTE  |
| ChangeKey         | ETag  |

İlk contacts scope:

```text
FindItem contacts folder
GetItem contact
CreateItem contact
UpdateItem contact
DeleteItem contact
SyncFolderItems contacts
```

## 18. Out of Office Mapping

Out of office için en pratik backend Sieve vacation’dır.

```text
EWS OOF settings
  -> Sieve vacation rule
```

Örnek Sieve:

```sieve
require ["vacation"];

vacation
  :days 1
  :subject "Out of office"
  "I am currently out of office.";
```

## 19. ResolveNames ve Address Book

Outlook kişi arama, recipient resolving ve address book için `ResolveNames` çağırabilir.

Backend seçenekleri:

```text
LDAP
iRedMail SQL users
SOGo address book
CardDAV contacts
```

İlk sürümde sadece local domain kullanıcıları döndürülebilir.

Sonraki sürümlerde:

- Domain users
- Personal contacts
- Shared address book
- GAL benzeri liste
- Distribution list mapping

## 20. Free / Busy

Free/busy için SOGo CalDAV üzerinden calendar event’ları okunur ve EWS `GetUserAvailability` response’una çevrilir.

İlk sürümde:

```text
GetUserAvailability
  -> kullanıcının default calendar'ını oku
  -> busy time slot üret
```

Daha sonra:

- Multiple calendar desteği
- Tentative / busy / OOF ayrımı
- Attendee availability
- Timezone handling

## 21. Outlook Request Logger

Outlook ile uyumluluk için request/response loglama kritik bir geliştirme aracıdır.

Log yapısı:

```text
var/log/ews-requests/
  2026-05-20-10-12-01-Autodiscover.xml
  2026-05-20-10-12-02-GetFolder.xml
  2026-05-20-10-12-03-FindFolder.xml
  2026-05-20-10-12-04-SyncFolderItems.xml

var/log/ews-responses/
  2026-05-20-10-12-01-Autodiscover.xml
  2026-05-20-10-12-02-GetFolder.xml
  2026-05-20-10-12-03-FindFolder.xml
  2026-05-20-10-12-04-SyncFolderItems.xml
```

Loglarda maskelenmesi gereken alanlar:

- Authorization header
- Password
- Cookies
- Session token
- Private message body, opsiyonel olarak

Örnek logger:

```go
type EwsRequestLogger struct {
    logDir string
    masker SensitiveDataMasker
}

func (l EwsRequestLogger) Log(operation string, xml []byte) error {
    safeXML := l.masker.MaskXML(xml)
    name := sanitizeOperationName(operation)
    filename := filepath.Join(l.logDir, time.Now().Format("2006-01-02-15-04-05")+"-"+name+".xml")

    return os.WriteFile(filename, safeXML, 0o600)
}
```


### 21.1 Logging privacy modu

Request logger Outlook uyumluluğu için gereklidir, ancak production ortamında kişisel veri sızıntısı üretmemelidir.

Log modları:

| Mod        | Davranış                                                                      |
|------------|-------------------------------------------------------------------------------|
| production | Authorization, password, cookie, token, body ve attachment içeriği maskelenir |
| debug      | Body loglama açıkça etkinleştirilebilir; attachment content yine loglanmaz    |

Ek kurallar:

- Authorization header her zaman maskelenir.
- Password ve token alanları her zaman maskelenir.
- Attachment content hiçbir modda loglanmaz.
- Log retention config ile belirlenir.
- Log dosya adı operation adı ve zaman bilgisinden oluşur; kullanıcı parolası, token veya ham e-posta adresi dosya adına yazılmaz.

## 22. Test Stratejisi

### 22.1 Fixture bazlı test

Outlook’tan gelen gerçek request’ler fixture olarak saklanır.

```text
tests/fixtures/requests/GetFolder-inbox.xml
tests/fixtures/responses/GetFolder-inbox.xml
```

Her operasyon için test:

```text
Given: Outlook SOAP request
When: Operation handler çalışır
Then: EWS compatible SOAP response döner
```

### 22.2 Integration test

Gerçek backend’lerle test:

- Dovecot IMAP test mailbox
- Postfix submission test account
- SOGo CalDAV test calendar
- SOGo CardDAV test address book
- ManageSieve test account

### 22.3 Outlook client test matrisi

Test edilecek istemciler:

```text
Outlook for Windows
Outlook for Mac
Apple Mail
Evolution
eM Client
Thunderbird + EWS plugin, varsa
```

Öncelik:

```text
1. Outlook for Windows
2. Outlook for Mac
3. Apple Mail
```

### 22.4 Outlook test akışı

Gerçek Outlook istemcisiyle test, fixture üretimiyle birlikte yürütülmelidir.

İlk test akışı:

```text
1. Outlook hesap ekleme ekranında e-posta ve parola girilir.
2. Autodiscover request loglanır ve fixture’a dönüştürülür.
3. EWS GetServerTimeZones request loglanır.
4. GetFolder ve FindFolder request’leri loglanır.
5. Inbox için FindItem request’i loglanır.
6. Bir mesaj açılarak GetItem request’i loglanır.
7. Read/unread değişikliğiyle UpdateItem request’i loglanır.
8. Test mail gönderimiyle CreateItem veya SendItem request’i loglanır.
```

Her yeni Outlook request tipi önce fixture testine eklenmeli, sonra operation handler davranışı sabitlenmelidir.

## 23. Hata Yönetimi

EWS tarafında hatalar SOAP fault veya operation-specific error response olarak dönmelidir.

Örnek hata sınıfları:

```text
ErrorInvalidIdMalformed
ErrorItemNotFound
ErrorFolderNotFound
ErrorAccessDenied
ErrorInternalServerError
ErrorInvalidCredentials
ErrorInvalidOperation
ErrorUnsupportedPathForQuery
```

Gateway, backend hatalarını EWS hata kodlarına map eder.

Örnek:

| Backend hatası          | EWS hatası               |
|-------------------------|--------------------------|
| IMAP auth failed        | ErrorInvalidCredentials  |
| IMAP folder not found   | ErrorFolderNotFound      |
| IMAP UID not found      | ErrorItemNotFound        |
| SMTP send failed        | ErrorInternalServerError |
| CalDAV 404              | ErrorItemNotFound        |
| ManageSieve auth failed | ErrorAccessDenied        |

## 24. Güvenlik

### 24.1 TLS

Tüm endpoint’ler HTTPS üzerinden çalışmalıdır.

### 24.2 Authentication

İlk sürümde Basic auth desteklenebilir.

Sonraki aşamada:

- App password
- OAuth2 proxy
- Per-user EWS enable/disable
- Per-domain EWS enable/disable
- Rate limiting
- Fail2ban uyumlu log formatı

### 24.3 Input validation

- XML entity expansion kapalı olmalı.
- External entity loading kapalı olmalı.
- SOAP body boyutu limitlenmeli.
- Attachment boyutu limitlenmeli.
- Folder ve item id decode edilirken kullanıcı sınırı kontrol edilmeli.

### 24.4 Authorization

Her EWS ItemId ve FolderId kullanıcının kendi mailbox’ına ait olmalıdır.

Gateway şu kontrolü her zaman yapmalıdır:

```text
request user == mapped item user
request user == mapped folder user
```

## 25. Deployment

### 25.1 Go servis deployment modeli

Gateway tek binary olarak çalışır ve systemd ile yönetilir. Nginx veya Apache yalnızca TLS termination ve reverse proxy için kullanılmalıdır.

Örnek binary konumu:

```text
/opt/iredmail-exchange-gateway/bin/gateway
/opt/iredmail-exchange-gateway/config/config.yaml
/var/log/iredmail-exchange-gateway
```

### 25.2 Systemd örneği

```ini
[Unit]
Description=iRedMail Exchange Compatibility Gateway
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
User=iredmail-exchange-gateway
Group=iredmail-exchange-gateway
ExecStart=/opt/iredmail-exchange-gateway/bin/gateway -config /opt/iredmail-exchange-gateway/config/config.yaml
Restart=on-failure
RestartSec=5s
NoNewPrivileges=true
PrivateTmp=true
ProtectSystem=strict
ReadWritePaths=/var/log/iredmail-exchange-gateway

[Install]
WantedBy=multi-user.target
```

### 25.3 Nginx reverse proxy örneği

```nginx
location = /EWS/Exchange.asmx {
    proxy_pass http://127.0.0.1:8080;
    proxy_set_header Host $host;
    proxy_set_header X-Forwarded-Proto https;
    proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;
}

location = /autodiscover/autodiscover.xml {
    proxy_pass http://127.0.0.1:8080;
    proxy_set_header Host $host;
    proxy_set_header X-Forwarded-Proto https;
    proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;
}

location = /Autodiscover/Autodiscover.xml {
    proxy_pass http://127.0.0.1:8080;
    proxy_set_header Host $host;
    proxy_set_header X-Forwarded-Proto https;
    proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;
}

location /mapi/ {
    proxy_pass http://127.0.0.1:8080;
    proxy_set_header Host $host;
    proxy_set_header X-Forwarded-Proto https;
    proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;
}

location /rpc/ {
    proxy_pass http://127.0.0.1:8080;
    proxy_set_header Host $host;
    proxy_set_header X-Forwarded-Proto https;
    proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;
}
```

## 26. Konfigürasyon

Örnek `config.yaml`:

```yaml
base_url: https://mail.example.com
listen_addr: 127.0.0.1:8080

auth:
  driver: dovecot

imap:
  host: 127.0.0.1
  port: 993
  encryption: ssl

smtp:
  host: 127.0.0.1
  port: 587
  encryption: tls

sogo:
  base_dav_url: https://mail.example.com/SOGo/dav

sieve:
  host: 127.0.0.1
  port: 4190

folders:
  inbox: INBOX
  sentitems: Sent
  drafts: Drafts
  deleteditems: Trash
  junkemail: Junk
  outbox: Outbox

logging:
  requests: true
  responses: true
  log_dir: /var/log/iredmail-exchange-gateway
```


## 27. Sprint Planı

### Sprint 1: Temel iskelet

Hedef:

- Go module ve project skeleton
- Native HTTP router
- Request/Response modelleri
- Basic auth
- Autodiscover endpoint
- EWS endpoint
- Go XML SOAP parser
- SOAP response builder
- Request logger

Çıktı:

```text
Outlook Autodiscover endpoint'i çağırabilir.
Gateway gelen EWS SOAP operasyonunu tanıyabilir.
Loglar dosyaya yazılır.
```

### Sprint 2: Folder desteği

Hedef:

- IMAP bağlantısı
- Folder mapping
- GetFolder
- FindFolder
- Distinguished folder id desteği
- FolderId üretimi
- ews_folder_map tablosu

Çıktı:

```text
Outlook klasör ağacını görebilir.
Inbox, Sent, Drafts, Trash, Junk listelenir.
```

### Sprint 3: Mail listeleme ve okuma

Hedef:

- FindItem
- GetItem
- IMAP SEARCH / FETCH
- MIME parser
- Body preview
- Attachment metadata
- ItemId üretimi
- ews_item_map tablosu

Çıktı:

```text
Outlook mailbox içeriğini listeleyebilir ve mail okuyabilir.
```

### Sprint 4: Sync

Hedef:

- SyncFolderItems
- Sync state tablosu
- IMAP UID / UIDVALIDITY / MODSEQ takibi
- Changed / deleted / new item ayrımı
- ChangeKey üretimi

Çıktı:

```text
Outlook değişiklikleri tekrar tekrar tam liste çekmeden takip edebilir.
```

### Sprint 5: Mail gönderme

Hedef:

- CreateItem
- SendItem
- MIME builder
- SMTP submission
- Sent klasörüne kaydetme
- Drafts klasörüne kaydetme

Çıktı:

```text
Outlook üzerinden mail gönderilebilir.
Draft oluşturulabilir.
```

### Sprint 6: Delete, move, update

Hedef:

- DeleteItem
- MoveItem
- UpdateItem
- Read/unread mapping
- Trash handling
- IMAP MOVE fallback
- Item map güncelleme

Çıktı:

```text
Outlook üzerinden mail silme, taşıma ve okundu/okunmadı işlemleri çalışır.
```

### Sprint 7: Attachments

Hedef:

- CreateAttachment
- GetAttachment
- DeleteAttachment
- MIME part extraction
- Attachment size limit
- Attachment streaming

Çıktı:

```text
Outlook attachment okuyabilir ve gönderebilir.
```

### Sprint 8: Rules

Hedef:

- GetInboxRules
- UpdateInboxRules
- Rule model
- Sieve parser
- Sieve generator
- ManageSieve upload
- Supported/unsupported rule ayrımı

Çıktı:

```text
Outlook’tan desteklenen mail kuralları yönetilebilir.
Kurallar Sieve’e çevrilir.
```

### Sprint 9: Calendar

Hedef:

- Calendar folder mapping
- CalDAV adapter
- ICS parser/generator
- Calendar FindItem
- Calendar GetItem
- Calendar CreateItem
- Calendar UpdateItem
- Calendar DeleteItem

Çıktı:

```text
Outlook takvim öğelerini görebilir, oluşturabilir, güncelleyebilir ve silebilir.
```

### Sprint 10: Contacts

Hedef:

- Contacts folder mapping
- CardDAV adapter
- vCard parser/generator
- Contact FindItem
- Contact GetItem
- Contact CreateItem
- Contact UpdateItem
- Contact DeleteItem

Çıktı:

```text
Outlook kişiler ekranı SOGo/CardDAV ile çalışır.
```

### Sprint 11: OOF, ResolveNames, FreeBusy

Hedef:

- OOF settings
- Sieve vacation mapping
- ResolveNames
- Local address book
- GetUserAvailability
- SOGo calendar busy slot mapping

Çıktı:

```text
Outlook recipient resolving, OOF ve free/busy fonksiyonlarını kullanabilir.
```

### Sprint 12: Stabilizasyon

Hedef:

- Outlook test matrisi
- Edge case düzeltmeleri
- Performance profiling
- Rate limiting
- Fail2ban log formatı
- Admin config
- Packaging

Çıktı:

```text
Gateway production test ortamına kurulabilir hale gelir.
```

### Sprint 13: Internal MAPI-like Store API

Hedef:

- MailboxStore
- FolderStore
- MessageStore
- PropertyBag
- EntryIdMapper
- ChangeTracker
- SessionStore
- EWS handler’larının bu store API üzerinden çalışacak şekilde ayrıştırılması

Çıktı:

```text
EWS operasyonları doğrudan backend adapter’larına değil, ortak internal store API’ye bağlanır.
Bu katman MAPI/HTTP ve RPC/HTTP için temel oluşturur.
```

### Sprint 14: MAPI property ve table modeli

Hedef:

- MAPI property tag modeli
- Named properties
- Folder hierarchy table
- Folder contents table
- Recipient table
- Attachment table
- EntryId üretimi ve çözümü
- Change number takibi

Çıktı:

```text
IMAP ve MIME verileri MAPI property bag ve table row formatına çevrilebilir.
```

### Sprint 15: MAPI/HTTP PoC

Hedef:

- `/mapi/emsmdb` endpoint
- `/mapi/nspi` endpoint
- MAPI/HTTP session yönetimi
- Mailbox open akışı
- Folder hierarchy table response
- Inbox contents table response
- Message property read response

Çıktı:

```text
Outlook MAPI/HTTP endpoint’ine bağlanabilir, mailbox açabilir, klasörleri ve inbox içeriğini okuyabilir.
```

### Sprint 16: RPC/HTTP PoC

Hedef:

- `/rpc/rpcproxy.dll` endpoint
- RPC over HTTP transport parser
- RPC context handle yönetimi
- MAPI RPC operation mapper
- Outlook Anywhere Autodiscover bilgisi

Çıktı:

```text
Eski Outlook istemcileri RPC/HTTP transport üzerinden internal MAPI-like Store API’ye ulaşabilir.
```


## 28. Minimum MVP

MVP için en küçük anlamlı kapsam:

```text
1. Autodiscover
2. Basic auth
3. GetServerTimeZones
4. GetFolder
5. FindFolder
6. FindItem
7. GetItem
8. SyncFolderItems
9. CreateItem
10. SendItem
11. DeleteItem
12. MoveItem
13. UpdateItem read/unread
14. Outlook request logger
```

Bu MVP tamamlandığında hedef:

```text
Outlook hesabı eklenebilir.
Klasörler görünür.
Mail listelenir.
Mail okunur.
Mail gönderilir.
Mail silinir.
Mail taşınır.
Okundu/okunmadı durumu çalışır.
```

## 29. Ürün Konumlandırması

Projenin adı:

```text
iRedMail Exchange Compatibility Gateway
```

Açıklama:

```text
Outlook ile iRedMail arasında çalışan Exchange uyumluluk katmanı.
MVP aşamasında Autodiscover ve EWS sağlar; ileri fazlarda MAPI/HTTP ve RPC/HTTP bağlantı türlerini aynı iRedMail backend servislerine map eder.
```

Desteklenen backend’ler:

```text
Dovecot IMAP
Postfix SMTP
SOGo CalDAV
SOGo CardDAV
Dovecot Pigeonhole Sieve
iRedMail SQL / LDAP
```

İlk hedeflenen client:

```text
Outlook for Windows
Outlook for Mac
Apple Mail
```

## 30. Kritik Tasarım Kararları

### 30.1 Backend Exchange ile değiştirilmez

Bu proje iRedMail backend’ini Exchange ile değiştirmez. Dovecot, Postfix, SOGo, Sieve ve auth backend’leri korunur. Outlook uyumluluğu gateway içinde sağlanır.

### 30.1.1 Exchange compatibility layer eklenir

EWS MVP’den sonra MAPI/HTTP ve RPC/HTTP desteklenecekse gateway içinde Exchange/MAPI semantiğine yakın bir compatibility layer gerekir. Bu katman backend servislerinin yerine geçmez; backend verilerini Outlook’un beklediği mailbox, folder table, contents table, property bag ve session modeline çevirir.

### 30.2 Backend’ler değiştirilmez

Dovecot, Postfix, SOGo ve Sieve mevcut halleriyle kullanılmalıdır. Gateway, bu servislerin üstünde çalışmalıdır.

### 30.3 ID/state gateway içinde tutulur

IMAP, CalDAV ve CardDAV EWS `ItemId`, `FolderId`, `ChangeKey` mantığını doğal olarak sağlamaz. MAPI/HTTP ve RPC/HTTP için de `EntryId`, named properties, change number ve session state doğal olarak sağlanmaz. Bu yüzden gateway kendi state katmanını tutmalıdır.

### 30.4 Outlook uyumluluğu fixture ile geliştirilir

Gerçek Outlook request’leri loglanmalı ve fixture testlerine dönüştürülmelidir.

### 30.5 Rules için Sieve esas alınır

Outlook Inbox Rules, desteklenen ölçüde Sieve’e çevrilir. Desteklenmeyen rule tipleri açıkça işaretlenir veya read-only gösterilir.

## 31. Açık Konular

Aşağıdaki konular geliştirme sırasında ayrıca netleştirilmelidir:

- Outlook’un hangi sürümü birincil hedef olacak?
- Basic auth yeterli mi, yoksa app password/OAuth2 gerekli mi?
- iRedMail SQL mi LDAP mı kullanılacak?
- SOGo zorunlu bağımlılık mı olacak?
- Sieve scripts tamamen gateway tarafından mı yönetilecek, yoksa mevcut script’lerle merge mi edilecek?
- Shared mailbox hedef kapsamda olacak mı?
- Takvim recurrence mapping ne kadar geniş desteklenecek?
- Attachment boyut limiti ne olacak?
- Admin panel ilk sürümde gerekli mi?
- Logging privacy seviyesi ne olacak?
- MAPI/HTTP için ilk hedef Outlook sürümü hangisi olacak?
- RPC/HTTP desteği gerçekten gerekli mi, yoksa MAPI/HTTP yeterli olacak mı?
- Internal MAPI-like Store API Go servis içinde mi kalacak, yoksa ayrı worker binary’lerine bölünecek mi?
- Notification modeli IMAP IDLE, polling veya ayrı event store ile mi uygulanacak?

## 32. Sonuç

Bu projenin net hedefi:

```text
Outlook <-> Exchange Compatibility Gateway <-> iRedMail servisleri
```

Gateway, MVP aşamasında Outlook’un EWS SOAP isteklerini alır ve iRedMail’in mevcut servislerine çevirir. İleri fazlarda MAPI/HTTP ve RPC/HTTP bağlantı türleri aynı backend servislerinin üstündeki internal MAPI-like Store API’ye bağlanır:

```text
Mail       -> Dovecot IMAP + Postfix SMTP
Calendar   -> SOGo CalDAV
Contacts   -> SOGo CardDAV
Rules      -> Dovecot Sieve / ManageSieve
Auth       -> iRedMail SQL / LDAP / Dovecot auth
State      -> Gateway database
```

İlk sürümde EWS tabanlı mail akışı tamamlanmalı, sonra rules, calendar ve contacts eklenmelidir. MAPI/HTTP ve RPC/HTTP ancak EWS MVP ve internal store API sağlamlaştıktan sonra eklenmelidir.

En doğru geliştirme yöntemi:

```text
1. Outlook request’lerini logla.
2. En sık kullanılan EWS operasyonlarını uygula.
3. Her operasyonu fixture testlerle sabitle.
4. Backend mapping’i modüler tut.
5. Önce mail, sonra rules, sonra calendar/contact ilerle.
6. Internal MAPI-like Store API’yi EWS MVP’den sonra çıkar.
7. MAPI/HTTP PoC’yi RPC/HTTP’den önce geliştir.
```

Bu şekilde proje, iRedMail’in mevcut güçlü servislerini koruyarak Outlook’a önce EWS uyumlu, sonra MAPI/HTTP ve RPC/HTTP destekli daha geniş bir Exchange compatibility arayüzü sağlar.
