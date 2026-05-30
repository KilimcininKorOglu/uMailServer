# TEST

## Kullanılacak yardımcı proje

- Tüm EWS son kullanıcı test scriptleri `helper-projects/exchangelib` kaynak ağacını doğrudan kullanır.
- Oluşturulan Python dosyaları:
  - `helper-projects/exchangelib_end_user_common.py`
  - `helper-projects/exchangelib_end_user_mail.py`
  - `helper-projects/exchangelib_end_user_rules.py`
  - `helper-projects/exchangelib_end_user_collab.py`

## Test hesapları

- `qa.alice@local.test` / `AlicePass123!` — ana test kullanıcısı
- `qa.bob@local.test` / `BobPass123!` — karşı taraf kullanıcı
- `qa.carol@local.test` / `CarolPass123!` — CC/BCC, yönlendirme ve kural doğrulama kullanıcısı
- `qa.shared@local.test` / `SharedPass123!` — shared mailbox / contact / calendar sahibi
- `admin@local.test` / `password` — mevcut bootstrap admin hesabı; paylaşımlı erişim grant açmadan önce şifre değişimi gerekebilir

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

- Bootstrap:
  - `python3 -m venv /tmp/umailserver-exchangelib`
  - `/tmp/umailserver-exchangelib/bin/python -m pip install -e helper-projects/exchangelib`
- Mail yaşam döngüsü:
  - `/tmp/umailserver-exchangelib/bin/python helper-projects/exchangelib_end_user_mail.py`
- Kural ve out-of-office:
  - `/tmp/umailserver-exchangelib/bin/python helper-projects/exchangelib_end_user_rules.py`
- Collaboration / contact / calendar / task:
  - `/tmp/umailserver-exchangelib/bin/python helper-projects/exchangelib_end_user_collab.py`
- Tek senaryo çalıştırma örnekleri:
  - `/tmp/umailserver-exchangelib/bin/python helper-projects/exchangelib_end_user_rules.py --scenario rule-live`
  - `/tmp/umailserver-exchangelib/bin/python helper-projects/exchangelib_end_user_collab.py --scenario shared-mailbox`

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

### Protokol kapsamı (en büyük boşluk — şu an yalnızca EWS test ediliyor)

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

### Otomasyon durumu

- Yukarıdaki üç script yalnızca EWS son kullanıcı akışlarını kapsar.
- "Genişletilmiş test kapsamı" başlığındaki maddeler henüz otomatik scriptlere
  bağlanmamıştır; bunlar için yeni istemciler/scriptler gerekir:
  - IMAP / POP3 / SMTP submission ve inbound için Python `imaplib`, `poplib`,
    `smtplib` (TLS varyantlarıyla)
  - ManageSieve için ham 4190 protokol istemcisi
  - CalDAV / CardDAV için HTTP (`requests`) veya `caldav` kütüphanesi
  - JMAP, MAPI/HTTP (NSPI + OAB) ve Autodiscover / Autoconfig için HTTP istemcisi
  - SPF / DKIM / DMARC, anti-spam, anti-virus, kota ve TLS politikası için
    sunucu yapılandırması + sentetik mesaj üretimi
  - Yedekleme / geri yükleme, cluster, audit ve metrics için CLI ve admin API
    tabanlı doğrulama
