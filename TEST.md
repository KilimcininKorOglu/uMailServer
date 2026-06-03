# TEST

## Kullanılacak yardımcı proje

- EWS son kullanıcı test scriptleri `helper-projects/exchangelib` kaynak ağacını doğrudan kullanır.
- EWS son kullanıcı scriptleri:
  - `helper-projects/exchangelib_end_user_common.py` (ortak yardımcılar; doğrudan çalıştırılmaz)
  - `helper-projects/exchangelib_end_user_mail.py`
  - `helper-projects/exchangelib_end_user_rules.py`
  - `helper-projects/exchangelib_end_user_collab.py`
- Protokol probe scriptleri (saf protokol istemcileri, `urllib`/`imaplib`/`poplib`/`smtplib` ile):
  - `helper-projects/proto_imap.py` (IMAP)
  - `helper-projects/proto_pop3.py` (POP3)
  - `helper-projects/proto_smtp.py` (SMTP submission + inbound)
  - `helper-projects/proto_managesieve.py` (ManageSieve)
  - `helper-projects/proto_caldav.py` (CalDAV)
  - `helper-projects/proto_carddav.py` (CardDAV)
  - `helper-projects/proto_mapi.py` (MAPI/HTTP — NSPI + OAB)
  - `helper-projects/proto_jmap.py` (JMAP)
  - `helper-projects/proto_autodiscover.py` (Autodiscover + Autoconfig)
  - `helper-projects/proto_cross.py` (protokoller arası tutarlılık)
- Ek probe'lar: `helper-projects/jmap_probe.py`, `helper-projects/jmap_send_probe.py`, `helper-projects/default_folders_probe.py`, `helper-projects/smime_probe.py`.
- Orkestratör: `helper-projects/run_all.py` tüm protokol + EWS suite'lerini sırayla çalıştırır ve özet basar.

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

- Not ekle
- Not güncelle
- Not sil
  - `helper-projects/exchangelib` tarafında first-class StickyNote item modeli olmadığı için bu bölüm şu an probe + manuel doğrulama gerektirir

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
- `helper-projects/proto_jmap.py` — JMAP: session/capability, Mailbox/get, Email/query+get+set
- `helper-projects/proto_autodiscover.py` — Autodiscover (Outlook) + Autoconfig (Thunderbird)
- `helper-projects/proto_cross.py` — protokoller arası tutarlılık (EWS/IMAP/POP3/JMAP aynı mesajı görür)
- `helper-projects/default_folders_probe.py` — standart klasörlerin tüm protokollerde görünürlüğü
- `helper-projects/smime_probe.py` — giden S/MIME imzalama

### Otomasyon durumu

- EWS son kullanıcı akışları + temel protokol kapsamı OTOMATİKTİR. `run_all.py`
  şu suite'leri çalıştırır: IMAP, SMTP, ManageSieve, JMAP, Autodiscover/Autoconfig,
  POP3, CalDAV, CardDAV, MAPI/HTTP, protokoller arası tutarlılık, ve üç EWS
  son kullanıcı suite'i (mail, kural/OOF, collaboration). Hepsi yeşil olmalı.
- Henüz otomatik scriptlere BAĞLANMAMIŞ "Genişletilmiş test kapsamı" maddeleri
  (yeni istemci/yapılandırma gerektirir):
  - TLS varyantları: STARTTLS (587/143/110/4190) ve implicit TLS (465/993/995/8443),
    sertifika/zincir/SAN ve TLS sürüm/cipher politikası
  - E-posta kimlik standartları: DKIM imzalama, gelen SPF/DKIM/DMARC değerlendirmesi
    ve `Authentication-Results`, DMARC politikası/raporları, ARC zinciri
  - Anti-spam / anti-virus: spam skorlama + junk yönlendirme, EICAR ile virüslü ek
    reddi, tehlikeli ek türü engelleme, gönderen bazlı rate-limit
  - Kota ve limitler: mailbox kota zorlaması (552), gönderim/saat limiti, klasör
    öğe sınırı
  - OpenPGP imzalı/şifreli mesaj akışı (S/MIME imzalama `smime_probe.py` ile var)
  - SMTP teslim derinliği: retry/deferral/bounce, DSN/NDR, MDN, döngü/hop tespiti,
    kuyruk inceleme (`queue` CLI)
  - Sistem operasyonları: yedekleme/geri yükleme (`backup` CLI), `db`/`account`/
    `domain` CLI ve admin API, cluster/failover, audit log, Prometheus metrics,
    sağlık ucu, webhook/alert
  - Kimlik doğrulama derinliği: LDAP, OAuth/modern auth, app password, oturum/token
    süre dolması; webmail push (WebSocket/SSE) bildirimleri
