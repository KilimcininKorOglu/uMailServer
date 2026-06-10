# TEST

## Kullanılacak yardımcı proje

- EWS son kullanıcı test scriptleri `helper-projects/exchangelib` kaynak ağacını doğrudan kullanır.
- EWS son kullanıcı scriptleri:
  - `helper-projects/exchangelib_end_user_common.py` (ortak yardımcılar; doğrudan çalıştırılmaz)
  - `helper-projects/exchangelib_end_user_mail.py`
  - `helper-projects/exchangelib_end_user_rules.py`
  - `helper-projects/exchangelib_end_user_collab.py`
- Protokol probe scriptleri (saf protokol istemcileri, `urllib`/`imaplib`/`poplib`/`smtplib` ile):
  - `helper-projects/proto_imap.py` (IMAP) + `proto_imap_ext.py` (IDLE/CONDSTORE/MOVE/SORT/THREAD/ESEARCH/MULTIAPPEND/ENABLE/NAMESPACE/ID/COMPRESS/ACL derinliği)
  - `helper-projects/proto_pop3.py` (POP3) + `proto_pop3_ext.py` (CAPA/NOOP/kilitlenme + DELE kalıcılığı)
  - `helper-projects/proto_smtp.py` (SMTP submission + inbound) + `proto_smtp_security.py` (SPF/DKIM/DMARC/ARC sonuçları, giden DKIM, DSN, BDAT/CHUNKING, AUTH mekanizmaları)
  - `helper-projects/proto_lmtp.py` (LMTP yerel teslim: LHLO selamlaması, EHLO'nun reddi, MAIL/RCPT/DATA, nokta sonrası alıcı-başına yanıt, mesajın posta kutusuna düşmesi; LMTP portu kapalıysa zarif SKIP)
  - `helper-projects/proto_managesieve.py` (ManageSieve) + `proto_sieve_exec.py` (fileinto/redirect/discard/reject/imap4flags/vacation'ın teslimde gerçekten çalışması)
  - `helper-projects/proto_caldav.py` (CalDAV), `proto_carddav.py` (CardDAV) + `proto_dav_ext.py` (ETag/CTag, If-Match 412, PROPPATCH, OPTIONS, VTODO, çapraz yüzey)
  - `helper-projects/proto_mapi.py` (MAPI/HTTP — NSPI + OAB) + `proto_mapi_ext.py` (GetGAL, object_class, 100 kaydı sınırı, HiddenFromGAL, OAB artımlı)
  - `helper-projects/proto_notes.py` (Outlook Notes) + `proto_notes_ext.py` (JMAP yazma yolu + ters görünürlük)
  - `helper-projects/proto_tnef.py` (gelen TNEF/winmail.dat çözümü: application/ms-tnef ekleri gerçek dosyalara açılır, üst-düzey TNEF gövdesi çözülür, HTML-kapsüllü PR_RTF_COMPRESSED gövdesi MS-OXRTFEX ile HTML'e de-encapsulate edilir; ayrıca giden TNEF üretimi: `export --tnef` ile dışa aktarılan .tnef yeniden teslim edilince gövde+ek geri çözülür)
  - `helper-projects/proto_jmap.py` (JMAP) + `proto_jmap_ext.py` (Thread/Identity/VacationResponse, Mailbox/Email değişiklikleri, blob, EventSource, SearchSnippet, takvim/kişi/not metotları)
  - `helper-projects/proto_autodiscover.py` (Autodiscover + Autoconfig) + `proto_autodiscover_ext.py` (EWS/MAPI/NSPI/OAB girdileri, devre dışı hesap 403)
  - `helper-projects/proto_ews_ext.py` (EWS derinliği: klasör yönetimi, Sync*, Subscribe/GetEvents, Availability, rooms, ExpandDL, MailTips, UserConfiguration, ConvertId, Persona/Photo)
  - `helper-projects/proto_cross.py` (protokoller arası tutarlılık)
- Yüzey/işletim probe'ları:
  - `helper-projects/proto_auth.py` (login kilitleme 429, TOTP yaşam döngüsü, JWT refresh/logout kara listesi, parola değişimi)
  - `helper-projects/proto_admin.py` (admin REST: domain/alias/grup CRUD, kuyruk, tenant, 401/403 kapıları)
  - `helper-projects/proto_reload.py` (config hot-reload: canlı POP3 kapatma/açma, restart_required sınıflaması, DTO'da sır yok)
  - `helper-projects/proto_recoverable.py` (Recoverable Items soft-delete dumpster: admin config ile aç, kalıcı sil → "Recoverable Items" klasöründe webmail+IMAP'te görün, INBOX'tan kalk, `/mail/recover` ile geri yükle; dumpster açılamazsa zarif SKIP)
  - `helper-projects/proto_quota.py` (kademeli posta kutusu kotası: admin API ile warn/prohibit-send/hard-cap eşikleri kur, inbound teslimle doldur → warn aşımında INBOX'a tek seferlik uyarı, prohibit-send aşımında webmail gönderim 502, hard-cap aşımında inbound teslim reddi; eşikler 0=devre dışı iken eski davranış korunur)
  - `helper-projects/proto_tls.py` (STARTTLS değişmezi: SMTP/IMAP/POP3'te ilan ⇔ el sıkışma başarısı, TLS>=1.2, DTO'da özel anahtar yok)
  - `helper-projects/proto_backup.py` (per-user yedek oluştur/listele/doğrula/güvenli geri yükle + push stub negatifleri)
  - `helper-projects/proto_import.py` (kanonik posta kutusu içe aktarımı: `umailserver import` ile mbox sunucu durdurulmuş koşulda içe aktarılır, idempotent yeniden çalıştırma, IMAP/JMAP/EWS çapraz görünürlük)
  - `helper-projects/proto_export.py` (kanonik posta kutusu dışa aktarımı: `umailserver export` ile mbox round-trip — import edilen folder export edilir, mboxrd `>From` escape ve mesaj bütünlüğü doğrulanır)
  - `helper-projects/proto_mbck.py` (posta kutusu tutarlılık denetimi + onarımı, üç senaryo: (1) orphan-delete — blob dizini silinince yetim index/identity tespit + `--repair` ile silme; (2) EWS-ghost recreate — izole bir data_dir'de semcore identity.db silinince eksik semcore identity'lerinin `--repair` ile yeniden oluşturulması; (3) orphan-semcore rebuild — izole data_dir'de IMAP index DB (mail/mail.db) silinince canlı blob+identity'li mesajların eksik IMAP index girdilerinin `--repair` ile yeniden kurulması)
  - `helper-projects/proto_mbck_pg.py` (PostgreSQL backend doğrulaması — standalone, run_all dışı: `import/export/check mailbox [--repair]` komutlarının throwaway bir postgres DB üzerinde çalıştığını kanıtlar; recreate senaryosu semcore identity tablolarını TRUNCATE ederek, orphan-semcore rebuild senaryosu `messages` tablosundan index satırlarını silerek tohumlanır)
  - `helper-projects/proto_pim.py` (PIM içe/dışa aktarım: `umailserver import --ics|--vcf` takvim etkinlikleri (VEVENT) + görevleri (VTODO) + kişileri kanonik collab store'a yazar, idempotent yeniden çalıştırma, webmail/collab API ile cross-protocol görünürlük (`/api/v1/calendar/events`, `/api/v1/tasks`, `/api/v1/contacts`), `umailserver export --ics|--vcf` round-trip)
  - `helper-projects/proto_pim_pg.py` (PIM içe/dışa aktarımın PostgreSQL backend doğrulaması — standalone, run_all dışı: throwaway postgres DB'de `import/export --ics|--vcf` round-trip)
  - `helper-projects/proto_mbsize.py` (posta kutusu boyut raporlama: `umailserver mbsize` per-folder/total mesaj sayısı + bayt, efektif kota, ve `QuotaUsed` sayaç sapmasını yüzeye çıkarır; `--domain` özeti)
  - `helper-projects/proto_mbsize_pg.py` (mbsize'ın PostgreSQL backend doğrulaması — standalone, run_all dışı)
  - `helper-projects/proto_metrics_mcp.py` (Prometheus /metrics içeriği + MCP JSON-RPC, token kapıları)
  - `helper-projects/smime_probe.py` (giden S/MIME imzalama — kendi kendine yeten: imzalamayı açar, anahtar üretir, container'ı yeniden oluşturur, sonda eski hâline döndürür)
- Ek probe'lar: `helper-projects/jmap_probe.py`, `helper-projects/jmap_send_probe.py`, `helper-projects/default_folders_probe.py`.
- Orkestratör: `helper-projects/run_all.py` tüm protokol + yüzey + EWS suite'lerini sırayla çalıştırır ve özet basar (31 suite, hepsi yeşil olmalı).
- BAĞIMSIZ (run_all dışında): `helper-projects/ha_probe.py` — HA/failover harness'i. Dev stack'i durdurur, `docker-compose.ha-full.yml` topolojisini SIFIRDAN kurar (repmgr+pgpool, Redis Sentinel, 2 düğüm + HAProxy), eşzamanlı boot / kuyruk tek-teslim / düğümler arası OOF dedup / node-kill / Redis failover / PG failover senaryolarını koşar, sonda HA stack'i söküp dev stack'i geri getirir. Host portları dev stack'le çakıştığı için run_all'a dahil değildir.

## Test hesapları

- `qa.alice@local.test` / `AlicePass123!` — ana test kullanıcısı
- `qa.bob@local.test` / `BobPass123!` — karşı taraf kullanıcı
- `qa.carol@local.test` / `CarolPass123!` — CC/BCC, yönlendirme ve kural doğrulama kullanıcısı
- `qa.shared@local.test` / `SharedPass123!` — shared mailbox / contact / calendar sahibi
- `admin@local.test` / `Admin123!` — admin hesabı (paylaşımlı erişim grant'ları için admin paneli)

## Ön koşullar

- Yerel stack çalışıyor olmalı: `make docker-run`
- EWS endpoint: `http://localhost:8088/EWS/Exchange.asmx`
- Shared testleri için `qa.alice@local.test` hesabına `qa.shared@local.test` üzerinde en az okuma + yazma erişimi verilmeli
  - Yönetim yüzeyi: `http://localhost:8444/admin/` veya `http://localhost:8088/admin/`
  - İlgili sayfa: Delegation
  - Gerekli grant:
    - Shared mailbox okuma
    - Shared mailbox yazma
    - Gerekirse send-as / send-on-behalf

## Script çalıştırma

- Sanal ortam: tüm scriptler `helper-projects/.venv/bin/python` ile çalıştırılır. Bu venv `exchangelib` ve bağımlılığı `cached_property` ile hazırdır (sistem `python3`'ü PEP 668 nedeniyle `pip install` engelleyebilir).
  - İlk kurulum gerekirse: `python3 -m venv helper-projects/.venv && helper-projects/.venv/bin/python -m pip install -e helper-projects/exchangelib cached_property`
- Tüm takım (önerilen):
  - `helper-projects/.venv/bin/python helper-projects/run_all.py`
- Mail yaşam döngüsü:
  - `helper-projects/.venv/bin/python helper-projects/exchangelib_end_user_mail.py`
- Kural ve out-of-office:
  - `helper-projects/.venv/bin/python helper-projects/exchangelib_end_user_rules.py`
- Collaboration / contact / calendar / task:
  - `helper-projects/.venv/bin/python helper-projects/exchangelib_end_user_collab.py`
- Protokol probe'ları (tekil):
  - `helper-projects/.venv/bin/python helper-projects/proto_imap.py` (aynısı pop3 / smtp / managesieve / caldav / carddav / mapi / jmap / autodiscover / cross için)
- Tek senaryo çalıştırma örnekleri:
  - `helper-projects/.venv/bin/python helper-projects/exchangelib_end_user_rules.py --scenario rule-live`
  - `helper-projects/.venv/bin/python helper-projects/exchangelib_end_user_collab.py --scenario shared-mailbox`

## Yapılandırma hot-reload (config hot-reload)

YAML ayarları üç yoldan canlı (sunucu restartı olmadan) güncellenebilir; her üçü
de aynı `Server.ReloadConfig` yolunu kullanır:

1. **Admin paneli** — Settings sayfasındaki tipli per-section formlar
   (`/api/v1/admin/config` PUT). Kayıttan sonra panel, hangi bölümlerin canlı
   uygulandığını (`applied`) ve hangilerinin restart gerektirdiğini
   (`restart_required`) banner ile gösterir.
2. **SIGHUP** — `kill -HUP <pid>` çalışan sunucuya YAML'ı yeniden okutur.
3. **Dosya-izleme** — `config.Watcher` dosyayı (mod-time + içerik hash'i) ile
   yoklar; YAML diskte elle değiştirilince otomatik reload tetiklenir.

Kategorizasyon: protokol dinleyicileri (SMTP/IMAP/POP3/ManageSieve/CalDAV/
CardDAV/JMAP/MCP/Metrics) canlı stop+start ile yeniden başlatılır; rate-limit
yöneticisi yerinde retune edilir; OOF/Notifications istek başına okunduğu için
yalnızca pointer swap yeter; yapısal/güvensiz bölümler (`server` data_dir/
hostname, `database`, `storage`, `tls` kimliği, `logging`, HTTP/Admin
dinleyicileri) ve sırlar `restart_required` olarak dürüstçe raporlanır. Sırlar
panelde HİÇ gösterilmez ve kayıtta korunur.

### Otomatik kapsam (Go testleri)

- `internal/api` — `TestConfigDTO_ExcludesSecrets` (DTO sır sızdırmaz),
  `TestApplyConfigDTO_PreservesSecretsAndAppliesEdits` (PUT sırları korur,
  düzenlemeyi uygular), `TestConfigDTO_RoundTrip` (düzenlemesiz GET→PUT hiçbir
  bölümü değiştirmez), `TestChangedSections`.
- `internal/server` — `TestReloadConfig_Classification` (oof/pop3 canlı,
  hostname restart), `TestReloadConfig_NoChangeIsNoop` (aynı config no-op —
  dosya-izlemenin kendi yazımına tepkisi zararsız).

### Canlı doğrulama (bayrak senaryo: POP3'ü kapat)

`make docker && make docker-stop && make docker-run` ile yığını yenile, sonra:

1. POP3 başta açık: `nc -z localhost 2995` bağlanır (veya
   `helper-projects/.venv/bin/python helper-projects/proto_pop3.py` yeşil).
2. Admin panelden (Services sekmesi) POP3'ü kapat ve kaydet. Yanıt
   `applied: [pop3]`, restart yok.
3. `nc -z localhost 2995` artık reddedilir; IMAP (`2143`) ve SMTP (`2525`)
   etkilenmez. Sunucu yeniden başlatılmadı.
4. POP3'ü tekrar aç → dinleyici geri gelir.
5. `kill -HUP` ve `docker-data` altındaki YAML'ı elle düzenleme de aynı reload'u
   tetikler (loglarda "Configuration reloaded").

## Son kullanıcı test listesi

### Mail akışları

- Mail gönderme
- Mail’e yanıt verme
- Mail’i klasöre taşıma
- Mail’i klasöre kopyalama
- Mail’i taslağa kaydetme
- Mail silme
- CC ve BCC doğrulama
- Flag / bayrak doğrulama
  - Okundu / okunmadı
  - Kategori ekleme / güncelleme
- Ekli mail gönderme
- Eki indirme
- Eki silme
- Inline image / CID içeren HTML mail
- Düz metin ve HTML mail görünümü
- Arama / filtre / sıralama
- Thread / konuşma görünümü tutarlılığı
- Büyük mail ve büyük ek sınırları
- Soft delete ve hard delete davranışı
- Junk / spam klasörü akışları
- Sent / Drafts / Trash klasör tutarlılığı
- Restart sonrası veri kalıcılığı
- Çoklu istemci tutarlılığı
  - EWS + webmail
  - EWS + IMAP

### Kural testleri

- Kural oluşturma: desteklenen her türü oluşturma
- Kuralları listeleme ve geri okuma
- Her kuralı canlı trafik ile doğrulama
- Kural sırasını / önceliğini doğrulama
- Kural silme ve devre dışı bırakma
- Gönderende metin içeriyor (`contains_sender_strings`)
- Alıcıda metin içeriyor (`contains_recipient_strings`)
- Konuda metin içeriyor (`contains_subject_strings`)
- Gövdede metin içeriyor (`contains_body_strings`)
- Konu veya gövdede metin içeriyor (`contains_subject_or_body_strings`)
- Bana gönderildi (`sent_to_me`)
- Ek içeriyor (`has_attachments`)
- Boyut aralığı (`within_size_range`)
- Klasöre taşı
- Sil
- Okundu olarak işaretle
- Başka alıcıya yönlendir / forward et
- Ekiyle forward et
- Redirect et
- Sonraki kuralları durdur
- Kategori ata (`X-Category` header doğrulaması)
- `copy_to_folder`
- `permanent_delete`
- `mark_importance`
- `server_reply_with_message`
- Kural çakışması / zincirleme çalışma
- Kural enable / disable akışları

### Out of office

- Out of office ekleme
- Out of office güncelleme
- Out of office devre dışı bırakma
- Out of office çalışmasını gerçek mail atarak doğrulama
- OOF zaman penceresi başlangıç ve bitiş sınırı

### Shared / collaboration

- Shared mailbox test etme
  - Listeleme
  - Okuma
  - Yazma
  - Gerekirse send-as / send-on-behalf
- Shared contact test etme
- Shared calendar test etme
- Shared mailbox erişim iptali sonrası yetki düşüşü

### Takvim

- Takvim öğesi ekle
- Takvim öğesi güncelle
- Takvim öğesi sil
- Takvim recurrence / tekrar eden toplantı akışları
- Toplantı daveti, kabul, reddet, tentative akışları

### Contact

- Contact ekle
- Contact güncelle
- Contact sil
- Contact arama ve `ResolveNames` doğrulaması

### Notlar

Notlar Exchange modeline uygun şekilde `IPM.StickyNote` sınıflı mesajlar olarak,
container sınıfı `IPF.StickyNote` olan Notes klasöründe tutulur; ayrı bir EWS Note
elemanı yoktur, not jenerik bir `<t:Message>` içinde `ItemClass=IPM.StickyNote`
ile taşınır. Notlar TEK kanonik kaynağı (Notes klasörü mesajları) paylaşır ve
TÜM yüzeylerde görünür: bir yüzeyde oluşturulan/silinen not diğerlerine yansır.
`helper-projects/proto_notes.py` raw EWS SOAP + IMAP + webmail + JMAP ile
otomatik kapsar:

- Not ekle — `CreateItem` (Message + `ItemClass=IPM.StickyNote`, Notes klasörüne)
- Not listele — `FindItem` notu `IPM.StickyNote` sınıfıyla döner
- Not oku — `GetItem` not konusu, gövdesi ve `ItemClass` değerini döndürür
- Not sil — `DeleteItem`; ardından `FindItem` notu listelemez
- `GetFolder(notes)` container sınıfını `IPF.StickyNote` olarak bildirir
- Çapraz-protokol: EWS ile oluşturulan not IMAP `Notes` klasöründe FETCH ile,
  webmail `GET /api/v1/notes`'ta ve JMAP `Note/get`'te görünür (Notes klasörü
  tüm depolarda provisyonlanır; EWS yazımları imap mailstore indeksine yansır)
- Yüzeyler: EWS (IPM.StickyNote), MAPI, IMAP (Notes klasörü), webmail (adanmış
  Notlar bölümü, `/api/v1/notes`), JMAP (`urn:umailserver:params:jmap:notes`
  capability + `Note/get|set|query|changes`). POP3 protokol gereği INBOX-only
- Not: `helper-projects/exchangelib` tarafında first-class StickyNote item modeli
  olmadığı için exchangelib uçtan-uca suite'i bu bölümü atlar; kapsam raw-SOAP
  `proto_notes.py` ile sağlanır

### Görevler

- Görev ekle
- Görev güncelle
- Görev sil

## Genişletilmiş test kapsamı (sistem ve Exchange yöneticisi gözüyle)

Aşağıdaki başlıklar yukarıdaki son kullanıcı listesini tamamlar. Tümü sunucunun
gerçekte sahip olduğu yeteneklere dayanır (`internal/` altında: smtp, imap, pop3,
sieve, caldav, carddav, jmap, ews, mapi, autoconfig, auth, av, spam, ratelimit,
quota, queue, backup, audit, webhook, alert, cluster, tls, push). EWS dışındaki
protokoller için ayrı istemciler gerekir (IMAP/POP3/SMTP için Python `imaplib`,
`poplib`, `smtplib`; CalDAV/CardDAV için `requests`/`caldav`; JMAP için HTTP).

### Protokol kapsamı (IMAP/POP3/SMTP/ManageSieve/CalDAV/CardDAV/MAPI/JMAP/Autodiscover artık `proto_*.py` ile otomatik)

- IMAP (143 / 993)
  - LOGIN, LIST, SELECT/EXAMINE, STATUS
  - FETCH (BODY, ENVELOPE, FLAGS, RFC822.SIZE), partial/ranged FETCH
  - STORE bayrak değişimi (`\Seen`, `\Flagged`, `\Deleted`), EXPUNGE
  - APPEND ile mesaj ekleme, COPY/MOVE
  - UIDVALIDITY ve UID kararlılığı (restart sonrası UID değişmemeli)
  - IDLE (push), yeni mesajda bildirim
  - CONDSTORE / QRESYNC (varsa MODSEQ tutarlılığı)
  - SUBSCRIBE / UNSUBSCRIBE, LSUB
  - Çoklu eşzamanlı IMAP oturumu tutarlılığı
- POP3 (995)
  - USER/PASS, STAT, LIST, UIDL
  - RETR, TOP, DELE, RSET, QUIT commit davranışı
  - Sunucuda bırak (leave-on-server) ve indirip silme
  - UIDL kararlılığı restart sonrası
- SMTP submission (587 STARTTLS / 465 implicit TLS)
  - AUTH PLAIN / LOGIN / CRAM-MD5
  - STARTTLS zorunluluğu, TLS olmadan AUTH reddi
  - Mesaj gönderimi, SMTPUTF8 ve 8BITMIME
  - Mesaj boyutu limiti (SIZE), alıcı sayısı limiti
  - PIPELINING
- SMTP inbound (25)
  - Dış kaynaktan yerel teslim
  - Open-relay reddi (yetkisiz relay engellenmeli)
  - Geçersiz alıcıda 550, var olmayan domain reddi
  - MAIL FROM / RCPT TO doğrulama
- ManageSieve (4190)
  - CAPABILITY, STARTTLS, AUTHENTICATE
  - PUTSCRIPT / GETSCRIPT / LISTSCRIPTS / CHECKSCRIPT
  - SETACTIVE / DELETESCRIPT
  - Hatalı sözdiziminin reddi
- CalDAV
  - PROPFIND (calendar-home-set, collection keşfi)
  - REPORT (calendar-query, calendar-multiget)
  - sync-collection, ETag ve değişiklik takibi
  - Free/busy sorgusu
  - VEVENT oluştur/güncelle/sil ve EWS ile tutarlılık
- CardDAV
  - PROPFIND (addressbook-home-set)
  - addressbook-query, sync-collection
  - vCard oluştur/güncelle/sil ve EWS contact ile tutarlılık
- JMAP
  - Session kaynağı, capability
  - Mailbox/get, Email/query + Email/get, Email/set
  - Push (varsa) bildirimleri
- MAPI/HTTP (NSPI + OAB)
  - NSPI üzerinden GAL araması / çözümleme
  - OAB indirme ve güncelleme
- Autodiscover ve Autoconfig
  - Outlook `autodiscover.xml` doğru protokol ve endpoint döndürmeli
  - Thunderbird autoconfig profili doğru olmalı
- Protokoller arası tutarlılık
  - Aynı mesaj/klasör/bayrak durumu EWS, IMAP, POP3, JMAP ve webmail’de aynı görülmeli
  - Bir protokolde okundu/silme diğerine yansımalı

### Kimlik doğrulama ve oturum güvenliği

- Başarılı ve başarısız giriş, hatalı parola davranışı
- Brute-force koruması / rate-limit / hesap kilitleme (`internal/ratelimit`)
- Parola değişimi sonrası eski parolanın reddi
- Parola politikası zorlaması
- LDAP ile kimlik doğrulama (`internal/auth/ldap`)
- OAuth / modern auth akışı
- Protokol bazında ayrı kimlik doğrulama (IMAP, SMTP, EWS, HTTP)
- Oturum / token süresi dolması ve iptali
- App password / uygulama parolası akışı

### Taşıma güvenliği (TLS)

- STARTTLS: SMTP (587), IMAP (143), POP3, ManageSieve (4190)
- Implicit TLS: 465, 993, 995, HTTPS admin (8443)
- Sunucu sertifikası, zincir ve SAN doğrulaması
- TLS sürüm politikası (TLS 1.2+ zorlama), zayıf cipher reddi
- Sertifika yenileme sonrası servis sürekliliği

### SMTP teslim, kuyruk ve raporlar (admin derinliği)

- Giden teslim, MX çözümleme
- Retry / deferral ve N denemeden sonra bounce
- DSN / NDR üretimi: 550, kota dolu, geçersiz alıcı
- MDN (okundu / teslim raporu) gönderme ve işleme (`internal/queue/mdn`)
- Mesaj döngüsü ve maksimum hop (Received zinciri) tespiti
- Kuyruk inceleme / temizleme (`queue` CLI) ve restart sonrası kuyruk kalıcılığı
- Alias, catch-all ve dağıtım listesi teslimi (`internal/api/server_aliases`)
- Mesaj boyutu ve alıcı sayısı limitleri

### E-posta kimlik doğrulama standartları

- Giden mesajlarda DKIM imzalama (seçici ve anahtar doğrulaması)
- Gelen mesajlarda SPF / DKIM / DMARC değerlendirmesi ve `Authentication-Results`
- DMARC politikası (none / quarantine / reject) uygulanması ve rapor üretimi
- ARC zinciri doğrulaması (`internal/auth/arc`)

### Anti-spam, anti-virus ve içerik güvenliği

- Spam skorlama ve junk klasörüne yönlendirme (`internal/spam`)
- Anti-virus taraması, EICAR test imzasıyla virüslü ek reddi (`internal/av`)
- Tehlikeli ek türü (örneğin yürütülebilir) engelleme
- Gönderen bazında gönderim rate-limit

### Kota ve limitler

- Mailbox kota zorlaması ve uyarı eşiği (`internal/quota`)
- Kota aşımında teslim reddi (552) ve kullanıcıya bilgi
- Mesaj / saat gönderim limiti
- Klasör başına öğe sayısı sınırı

### Şifreleme (S/MIME ve OpenPGP)

- S/MIME imzalı ve şifreli mesaj gönderme / alma (`internal/smtp/smime_stage`)
- OpenPGP imzalı ve şifreli mesaj akışı (`internal/smtp/openpgp_stage`)
- Sunucu tarafı çözme sonrası gövdenin doğru teslimi
- Geçersiz imza / yanlış anahtar davranışı

### Takvim derinliği (Exchange)

- Free/busy sorgusu (`GetUserAvailability`)
- Toplantı güncelleme, iptal, yeniden planlama ve counter-proposal
- Tekrar eden seride tek örnek düzenleme / silme (exception)
- Oda / kaynak mailbox rezervasyonu ve auto-accept / auto-decline politikası
- Delegate adına toplantı düzenleme ve davet yönlendirme modu (DelegatesAndMe vb.)
- Hatırlatıcılar, tüm gün etkinlik, zaman dilimi ve DST geçişleri
- Çakışan rezervasyonun reddi

### GAL ve adres defteri

- NSPI üzerinden GAL araması ve OAB indirme / güncelleme
- `ResolveNames` ile global çözümleme
- Dağıtım grupları ve kişi grupları
- Kişi fotoğrafı ve genişletilmiş kişi alanları

### Delegasyon derinliği

- Full Access, Send-As ve Send-on-Behalf ayrımının net doğrulanması
- Klasör seviyesi izinler (Reviewer / Author / Editor / Owner)
- Private (özel) öğe görünürlüğü
- Delegate toplantı-isteği teslim modları
- Çoklu delegate, grant güncelleme ve iptal sonrası anlık yetki düşüşü

### Arama derinliği

- Gövde tam metin ve ek içeriği araması
- Çoklu kriter (AND / OR), tarih aralığı, klasörler arası arama
- Türkçe karakter ve İ/ı büyük-küçük harf duyarlılığı doğrulaması
- Arama klasörleri / kalıcı aramalar

### Uluslararasılaştırma (i18n)

- Konu, gövde, görünen ad ve klasör adında Türkçe karakter (ç ğ ı İ ö ş ü)
- UTF-8 ek dosya adı (RFC 2231) ve encoded-word başlık (`=?UTF-8?...`)
- IDN / EAI uluslararası e-posta adresi ve SMTPUTF8 teslimi

### Sağlamlık ve uç durumlar

- Boş konu / gövde, çok uzun konu, hatalı biçimli MIME
- Yinelenen Message-ID, From yok veya çoklu From başlığı
- İç içe ve `message/rfc822` olarak iletilen mesaj
- Önem (importance) ve duyarlılık (sensitivity) bayrakları
- İçerik-adresli depo dedup doğruluğu (aynı içerik çok alıcıya teslimde)
- Çok büyük mailbox’ta sayfalama doğruluğu

### Sistem operasyonları (admin)

- Yedekleme ve geri yükleme (`backup` CLI) ve geri yükleme bütünlüğü
- Veritabanı bakımı / migration (`db` CLI)
- Hesap oluştur / devre dışı bırak / sil (`account` CLI ve admin API)
- Domain yönetimi (`domain` CLI ve admin API)
- Eşzamanlı erişim, race ve kilitleme davranışı
- Cluster / failover ve circuit breaker davranışı (`internal/cluster`, `internal/circuitbreaker`)
- Sağlık ucu (`internal/health`) ve Prometheus metrics doğruluğu
- Audit log ve güvenlik olayları (`internal/audit`)
- Webhook ve alert tetikleme (DSN, rate-limit, güvenlik olayı)

### İstemci onboarding ve bildirim

- Outlook autodiscover ile otomatik profil kurulumu
- Thunderbird autoconfig ile otomatik profil kurulumu
- Webmail push bildirimleri (WebSocket / SSE) yeni mesajda tetiklenmeli

## Script eşleştirmesi

- `helper-projects/exchangelib_end_user_mail.py`
  - Mail gönderme
  - Yanıt
  - Taslak
  - Kopyalama
  - Taşıma
  - Silme
  - CC / BCC
  - Okundu bayrağı + kategori güncellemesi
- `helper-projects/exchangelib_end_user_rules.py`
  - Desteklenen kural koşullarını oluşturma
  - Desteklenen kural aksiyonlarını oluşturma
  - Canlı kural doğrulaması
  - Out-of-office doğrulaması
- `helper-projects/exchangelib_end_user_collab.py`
  - Contact CRUD
  - Calendar CRUD
  - Task CRUD
  - Shared mailbox / shared contact / shared calendar
  - Notes probe
- `helper-projects/proto_imap.py` — IMAP: LOGIN/LIST/SELECT/STATUS, FETCH, STORE bayrak, EXPUNGE, APPEND, COPY/MOVE, UID kararlılığı, IDLE, SUBSCRIBE/LSUB
- `helper-projects/proto_pop3.py` — POP3: USER/PASS, STAT/LIST/UIDL, RETR/TOP/DELE/RSET, leave-on-server, UIDL kararlılığı
- `helper-projects/proto_smtp.py` — SMTP submission (AUTH, gönderim, SIZE) + inbound (yerel teslim, relay reddi, geçersiz alıcı)
- `helper-projects/proto_managesieve.py` — ManageSieve: CAPABILITY/AUTHENTICATE, PUT/GET/LIST/CHECK/SETACTIVE/DELETE, hatalı script reddi
- `helper-projects/proto_caldav.py` — CalDAV: PROPFIND/REPORT, sync-collection/ETag, free/busy, VEVENT CRUD
- `helper-projects/proto_carddav.py` — CardDAV: PROPFIND, addressbook-query/sync, vCard CRUD
- `helper-projects/proto_mapi.py` — MAPI/HTTP: NSPI ResolveNames (prefix + tam adres) GAL araması, OAB indirme, Basic-auth gate
- `helper-projects/proto_notes.py` — Outlook Notes (IPM.StickyNote): GetFolder(notes) IPF.StickyNote, CreateItem/FindItem/GetItem/DeleteItem not yaşam döngüsü
- `helper-projects/proto_jmap.py` — JMAP: session/capability, Mailbox/get, Email/query+get+set
- `helper-projects/proto_autodiscover.py` — Autodiscover (Outlook) + Autoconfig (Thunderbird)
- `helper-projects/proto_cross.py` — protokoller arası tutarlılık (EWS/IMAP/POP3/JMAP aynı mesajı görür)
- `helper-projects/default_folders_probe.py` — standart klasörlerin tüm protokollerde görünürlüğü
- `helper-projects/proto_imap_ext.py` — IMAP derinliği: IDLE bildirimi, CONDSTORE/HIGHESTMODSEQ, MOVE/UIDPLUS, SORT/THREAD, SEARCH/ESEARCH, MULTIAPPEND, ENABLE, NAMESPACE, ID, COMPRESS, ACL, AUTHENTICATE
- `helper-projects/proto_smtp_security.py` — gelen SPF/DKIM/DMARC/ARC `Authentication-Results`, giden DKIM imzası, DSN, BDAT/CHUNKING, AUTH mekanizmaları
- `helper-projects/proto_lmtp.py` — LMTP (RFC 2033) yerel teslim: 220 selamı sonrası EHLO 500 ile reddedilir (LMTP yalnız LHLO konuşur), LHLO 250 yetenekleriyle kabul edilir, MAIL FROM/RCPT TO (yerel alıcı)/DATA, nokta sonrası alıcı-başına tam bir 2xx yanıt, mesaj alıcının posta kutusuna düşer (API ile doğrulanır); LMTP portu erişilemezse (varsayılan kapalı) zarif SKIP
- `helper-projects/proto_recoverable.py` — Recoverable Items soft-delete dumpster ("Recover Deleted Items From Server"): admin config API ile `recoverable_items` açılır, bob'a teslim edilen mesaj kalıcı silinir (webmail permanent delete) → cross-protocol "Recoverable Items" klasöründe HEM webmail API HEM IMAP'te görünür ve INBOX'tan kalkar; `POST /api/v1/mail/recover` ile INBOX'a geri yüklenir ve dumpster'dan temizlenir; sonunda özgün config geri yüklenir. Retention CLEANER'ın zaman-tabanlı purge'ü burada sınanmaz (retention tam-gün; saniyede yaşlandırılamaz — expiry filtresi `internal/db` `ListExpiredRecoverableItems` birim testiyle kapsanır). Dumpster açılamazsa (admin login yoksa) zarif SKIP
- `helper-projects/proto_quota.py` — kademeli posta kutusu kotası (warn / prohibit-send / prohibit-send-receive): admin API ile bir throwaway hesabın `quota_warn`/`quota_prohibit_send`/`quota_limit` eşikleri yazılır ve geri okunarak round-trip doğrulanır; inbound SMTP (port 25) ile ~6 KB'lık mesajlar teslim edilerek gözlemlenen `quota_used` (admin API'den geri okunur, böylece sunucunun eklediği başlık baytlarına dayanıklı) her bandın içine taşınır: warn altında bildirim YOK → warn aşımında INBOX'a TAM BİR "Mailbox quota warning" notu (`maybeWarnQuota`, `fileFolderCopy` ile dosyalanır, kotayı tekrar saymaz) → daha fazla teslimde latch sayesinde hâlâ TEK not; prohibit-send altında webmail `/api/v1/mail/send` 200, prohibit-send aşımında 502 (`quotaProhibitsSend`); hard cap aşımında inbound teslim reddi (451, `IncrementQuota`). Eşikler 0=devre dışı olan ikinci hesapta warn notu hiç çıkmaz, prohibit kapalıyken gönderim serbest kalır, ama hard cap inbound reddi korunur. Domain-default kompozisyonu burada değil `internal/db` `TestEffectiveQuotaThresholds` birim testinde kapsanır; hesaplar sonunda silinir
- `helper-projects/proto_sieve_exec.py` — Sieve aksiyonlarının teslimde yürütülmesi: fileinto/redirect/discard/reject/imap4flags/vacation (tek otomatik yanıt)
- `helper-projects/proto_jmap_ext.py` — JMAP derinliği: Thread/Identity/VacationResponse, Mailbox query+set+changes, Email changes+import, blob upload/download, EventSource, SearchSnippet, takvim/kişi/not metotları
- `helper-projects/proto_pop3_ext.py` — POP3 derinliği: CAPA/NOOP, kimlik kilitleme, DELE+QUIT kalıcılığı
- `helper-projects/proto_dav_ext.py` — CalDAV/CardDAV derinliği: ETag/CTag değişimi, If-Match/If-None-Match 412, PROPPATCH, OPTIONS, VTODO, çapraz yüzey görünürlüğü
- `helper-projects/proto_mapi_ext.py` — MAPI/HTTP derinliği: GetGAL, object_class, 100 kayıt sınırı, HiddenFromGAL, OAB artımlı indirme
- `helper-projects/proto_notes_ext.py` — Notes JMAP yazma yolu + EWS/JMAP ters görünürlük
- `helper-projects/proto_tnef.py` — gelen TNEF (winmail.dat) çözümü: multipart/mixed içindeki application/ms-tnef ekleri gerçek dosyalara açılır (winmail.dat blob'u gizlenir, indirilen baytlar eşleşir), üst-düzey application/ms-tnef mesajın gövdesi çözülür; gövdesi yalnızca HTML-kapsüllü PR_RTF_COMPRESSED olan mesaj MS-OXRTFEX'e göre HTML'e de-encapsulate edilir (`<p>Hello world</p>`, ham RTF kontrol kelimeleri yüzeye çıkmaz). Giden TNEF üretimi de doğrulanır: ekli bir MIME mesajı teslim edilir, server durdurulup `umailserver export --user <e> --tnef <dir>` ile native TNEF'e dışa aktarılır, server başlatılıp dışa aktarılan .tnef application/ms-tnef olarak yeniden teslim edilir; server'ın kendi decoder'ı gövde+eki (`report.csv`) geri kurar — encoder'ın ürettiği akış uçtan uca round-trip eder.
- `helper-projects/proto_autodiscover_ext.py` — Autodiscover derinliği: EWS/MAPI/NSPI/OAB girdileri, devre dışı hesap 403, autoconfig auth/socket alanları
- `helper-projects/proto_ews_ext.py` — EWS derinliği: klasör yönetimi (oluştur/yeniden adlandır/sil), SyncFolderHierarchy/Items, Subscribe/GetEvents, GetUserAvailability, oda listeleri, ExpandDL, MailTips, UserConfiguration, ConvertId, GetPersona/GetUserPhoto
- `helper-projects/proto_auth.py` — kimlik doğrulama derinliği: login kilitleme 429, TOTP kurulum/doğrulama/kapatma + TOTP'li login, JWT refresh/logout kara listesi, parola değişimi
- `helper-projects/proto_admin.py` — admin REST API: domain/alias/mail-grubu CRUD, kuyruk listesi, tenant listesi, rate-limit DTO, 401/403 kapıları
- `helper-projects/proto_reload.py` — config hot-reload: POP3'ü canlı kapatıp açma, yapısal değişikliğin restart_required sınıflanması, GET DTO'da sır sızıntısı yok
- `helper-projects/proto_tls.py` — TLS/STARTTLS değişmezi: SMTP(25/587)/IMAP/POP3'te STARTTLS yalnızca el sıkışma başarılı olabilecekse ilan edilir; ilan yokken upgrade komutu reddedilir; min_version 1.2/1.3
- `helper-projects/proto_backup.py` — yedekleme yaşam döngüsü: per-user oluştur → listele → doğrula → farklı-kullanıcıya güvenli geri yükle → sil; POST /backups; push stub negatifleri (VAPID 503, SSRF koruması, 401/400/403)
- `helper-projects/proto_import.py` — kanonik posta kutusu içe aktarımı: test mbox'unu mount'lu veri dizinine yazar, `umailserver` container'ını durdurur, tek-seferlik container'da `umailserver import` çalıştırır (idempotensi için iki kez), sunucuyu yeniden başlatır, sonra içe aktarılan mesajların IMAP + JMAP + EWS'de göründüğünü doğrular (semcore identity yazımı = EWS hayalet koruması). Sunucu her durumda yeniden başlatılır.
- `helper-projects/proto_export.py` — kanonik posta kutusu dışa aktarımı (import'un tersi, round-trip): bilinen bir mbox'u benzersiz bir folder'a import eder, aynı folder'ı `umailserver export --mbox` ile dışa aktarır, sunucuyu yeniden başlatır, sonra host'taki dışa aktarılmış mbox'u ayrıştırıp iki mesajın da (konu + gövde + tam iki zarf ayracı + mboxrd `>From` escape'i) korunduğunu doğrular. Sunucu her durumda yeniden başlatılır.
- `helper-projects/proto_mbck.py` — posta kutusu tutarlılık denetimi ve onarımı (`umailserver check mailbox [--repair]`), üç senaryo, hepsi gerçek bbolt store'lara karşı uçtan uca: (1) ORPHAN-DELETE (paylaşımlı dev data_dir'i): throwaway bir hesap oluşturur, bilinen mbox'u import eder, denetimin TEMİZ olduğunu doğrular (üç kanonik store uyumlu = doğru semcore anahtarı, sahte ghost yok), sonra blob dizinini silip yetim girdilerin (orphan-index/identity) raporlandığını ve çıkış kodunun ≠0 olduğunu doğrular; `--repair` ile yetim IMAP index + semcore identity girdileri silinir ve yeniden TEMİZ döner. (2) EWS-GHOST RECREATE (izole data_dir): semcore identity.db tüm sunucu için tek dosya olduğundan, tek bir hesabın ghost'u paylaşımlı dizinde diğer posta kutularını silmeden tohumlanamaz; bu yüzden bu senaryo kendi `UMAILSERVER_CONFIG`'i ile İZOLE bir data_dir'de çalışır: import → TEMİZ → o dizinin `semcore/identity.db`'si silinir (güvenli, izole) → indexli+blob'lu mesajlar EWS-ghost olur → `--repair` semcore identity'lerini YENİDEN OLUŞTURUR → yeniden TEMİZ. (3) ORPHAN-SEMCORE REBUILD (izole data_dir, EWS-ghost'un tersi): import → TEMİZ → o dizinin IMAP index DB'si (`mail/mail.db`) silinir (hesap, bloblar ve ayrı semcore identity.db hayatta kalır) → her mesaj canlı blob+identity'li ama IMAP index'siz (orphan-semcore, IMAP/POP3'te görünmez) olur → `--repair` IMAP index girdilerini YENİDEN KURAR → yeniden TEMİZ. Sunucu her durumda yeniden başlatılır.
- `helper-projects/proto_mbck_pg.py` — PostgreSQL backend doğrulaması (standalone, run_all dışı; dev stack bbolt çalışır, bu probe `compose` `postgres` servisinde THROWAWAY bir DB oluşturup CLI'yi ona yönlendirir, sonra DB'yi düşürür — sunucunun verisine dokunmaz, server çalışmaya devam edebilir): `import` (postgres index + postgres semcore + Maildir blob 3-katman filing), `check` (uyumda TEMİZ), `export` (postgres-tabanlı index'ten mbox round-trip), `--repair` orphan-delete (blob dizini silinir → dangling index+identity silinir), `--repair` EWS-ghost recreate (semcore identity tabloları TRUNCATE edilir → identity'ler postgres index + Maildir blob'lardan yeniden oluşturulur) ve `--repair` orphan-semcore rebuild (`messages` tablosundan index satırları silinir → eksik IMAP index girdileri semcore identity + Maildir blob'lardan yeniden kurulur). Maildir gövdeleri backend'den bağımsız olarak dosya kalır; postgres yalnız metadata + identity tutar.
- `helper-projects/proto_pim.py` — PIM (takvim/görev/kişi) içe/dışa aktarımı: sunucuyu durdurur, throwaway bir hesap oluşturur, iki VEVENT + bir VTODO içeren bir `.ics` ve çok-VCARD'lı bir `.vcf` yazar, `umailserver import --ics`/`--vcf` ile kanonik collaboration store'a aktarır (`internal/pimport` UID-bazlı ayrıştırma; VEVENT → `caldav.CollabStore`, VTODO → `caldav.CollabTaskStore`, VCARD → `carddav.CollabStore`), aynı `.ics`'i yeniden import ederek idempotensiyi (etkinlik+görev UID'leri zaten varsa atla) doğrular, sunucuyu başlatır, sonra webmail/collab REST API'sinden (`/api/v1/calendar/events`, `/api/v1/tasks`, `/api/v1/contacts` — CalDAV/CardDAV/EWS/JMAP ile aynı kanonik store) iki etkinlik + bir görev + iki kişinin de göründüğünü (cross-protocol) doğrular; son olarak `umailserver export --ics`/`--vcf` ile round-trip yapar (`.ics` tek VCALENDAR'da iki VEVENT + bir VTODO + tek VTIMEZONE dedup; `.vcf` iki VCARD). Sunucu her durumda yeniden başlatılır.
- `helper-projects/proto_pim_pg.py` — PIM içe/dışa aktarımın PostgreSQL backend doğrulaması (standalone, run_all dışı; throwaway postgres DB): `import --ics`/`--vcf` postgres semcore collaboration tablolarına yazar, `export --ics`/`--vcf` round-trip doğrular. Aynı backend-agnostik `openMailCLIStores` yolunu kullanır (mbck/import/export ile ortak).
- `helper-projects/proto_mbsize.py` — posta kutusu boyut raporlama (`umailserver mbsize`): sunucuyu durdurur, throwaway bir hesaba bilinen bir mbox'u import eder, `mbsize <email>`'in INBOX'u 2 mesaj + doğru bayt ve TOTAL ile raporladığını doğrular; KRİTİK olarak, `import` `IncrementQuota` çağırmadığı için `QuotaUsed` sayacının 0 kaldığını ama hesaplanan boyutun >0 olduğunu — yani mbsize'ın kota sapmasını yüzeye çıkardığını — `NOTE: QuotaUsed counter is 0 B` çıktısıyla doğrular; `mbsize --domain local.test`'in hesabı listelediğini doğrular. Sunucu her durumda yeniden başlatılır.
- `helper-projects/proto_mbsize_pg.py` — mbsize'ın PostgreSQL backend doğrulaması (standalone, run_all dışı; throwaway postgres DB): `import` sonrası `mbsize`'ın per-folder/total figürlerini ve aynı kota-sapma NOTE'unu relational backend'de ürettiğini doğrular.
- `helper-projects/proto_metrics_mcp.py` — Prometheus /metrics içerik + MCP JSON-RPC (token kapısı, admin araç RBAC'ı)
- `helper-projects/proto_scheduled.py` — zamanlanmış ("sonra gönder") uçtan uca: webmail `sendAt` → Scheduled klasörü (API + IMAP) → lider-kapılı salıverme → teslim + Sent'e dosyalama; API iptal; Scheduled klasöründen IMAP EXPUNGE ile iptal; SMTP FUTURERELEASE (RFC 4865) EHLO ilanı + MAIL FROM HOLDFOR
- `helper-projects/proto_ews_scheduled.py` — EWS deferred-send (Outlook "Do not deliver before"): CreateItem'da PidTagDeferredSendTime (0x3FEF) → canonical scheduled store + EWS FindItem ile Scheduled klasöründe görünür + sunucu tarafından salıverilip teslim. NOT: deferred-send yalnızca EWS property seviyesinde simüle edilir; gerçek Outlook masaüstü gönderim yolu bu ortamda doğrulanamaz.
- `helper-projects/smime_probe.py` — giden S/MIME imzalama (kendi kendine yeten: signing'i açar, PKCS#1 anahtar üretir, container'ı yeniden oluşturur, sonda geri alır; run_all'da EN SONDA koşar)
- `helper-projects/ha_probe.py` — BAĞIMSIZ HA/failover harness'i (run_all dışı; ayrıntı yukarıda)

### Otomasyon durumu

- EWS son kullanıcı akışları + protokol kapsamı + yüzey/işletim kapsamı
  OTOMATİKTİR. `run_all.py` 31 suite çalıştırır: 11 temel protokol suite'i,
  bunların `_ext` derinlik suite'leri, Sieve yürütme, SMTP güvenlik, TLS/STARTTLS,
  auth/güvenlik, admin REST, config hot-reload, backup+push, Metrics+MCP, üç EWS
  son kullanıcı suite'i ve (en sonda, kendi kendine yeten) S/MIME imzalama.
  Hepsi yeşil olmalı.
- HA/failover ayrıca OTOMATİKTİR ama run_all dışındadır: `ha_probe.py`
  (`docker-compose.ha-full.yml` üzerinde eşzamanlı boot, kuyruk tek-teslim,
  düğümler arası OOF dedup, node-kill, Redis Sentinel failover, repmgr/pgpool
  PG failover + standby olarak yeniden katılma, sonda dev stack'in geri
  getirilmesi). Host portları dev stack ile çakıştığından ayrı çalıştırılır.
- Otomatik kapsama BAĞLANMIŞ eski eksikler: STARTTLS ilan değişmezi (`proto_tls.py`;
  gerçek el sıkışma dev'de cert olmadığı için yalnızca cert'li kurulumda),
  SPF/DKIM/DMARC/ARC + giden DKIM + DSN (`proto_smtp_security.py`), yedekleme/geri
  yükleme API'si (`proto_backup.py`), cluster/failover (`ha_probe.py`), Prometheus
  metrics + sağlık (`proto_metrics_mcp.py`), TOTP/JWT/parola akışları
  (`proto_auth.py`), admin API CRUD (`proto_admin.py`).
- Henüz otomatik scriptlere BAĞLANMAMIŞ maddeler (yeni istemci/yapılandırma
  gerektirir):
  - Implicit TLS dinleyicileri (465/993/995/8443) ve sertifika/zincir/SAN,
    cipher politikası — dev'de geçerli sertifika yok
  - Anti-spam / anti-virus: spam skorlama + junk yönlendirme, EICAR ile virüslü ek
    reddi (ClamAV dev'de kapalı), tehlikeli ek türü engelleme
  - Kota ve limitler: mailbox kota zorlaması (552), gönderim/saat limiti, klasör
    öğe sınırı
  - OpenPGP imzalı/şifreli mesaj akışı (S/MIME imzalama `smime_probe.py` ile var)
  - SMTP teslim derinliği: retry/deferral/bounce zamanlaması, MDN, döngü/hop
    tespiti, kuyruk inceleme (`queue` CLI)
  - Kimlik doğrulama derinliği: LDAP, OAuth/modern auth, app password; webmail
    push (WebSocket/SSE) bildirimleri
