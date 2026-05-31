# Webmail Test Raporu

Bu rapor, `webmail/` (son kullanıcı React istemcisi) üzerinde desteklenen her
özelliğin canlı Docker yığınında (`http://localhost:8088`) test edilmesiyle
hazırlanmıştır. Test hesabı: `qa.alice@local.test`. Doğrulama yöntemi: tarayıcı
otomasyonu (Playwright), gerçek ağ istekleri/yanıtları, DOM incelemesi ve kaynak
kod analizi.

## Test kapsamı

Login/oturum, Inbox, e-posta okuma/yanıtlama/iletme/silme, klasör görünümleri
(Starred/Sent/Drafts/Trash/Spam ve gerçek klasörler), Compose (gönderme, Cc/Bcc,
taslak, gönderen kimliği, biçimlendirme, ek), Search, Contacts, Filters, Settings
(tema, otomatik yanıt, yerel anahtarlar), header (arama, bildirim, kısayollar,
kullanıcı menüsü), Threads ve Push.

## Çalıştığı doğrulanan özellikler

- Geçerli kimlikle giriş ve `/inbox`'a yönlendirme.
- Inbox gerçek postaları listeliyor; bir mesajı açmak gövdeyi temiz (MIME
  başlıksız) gösteriyor ve sunucu tarafında okundu olarak işaretliyor.
- E-posta detayında Reply/Forward composer'ı ön-dolduruyor, Delete gerçekten
  siliyor (`api.deleteMail`).
- Klasör içerikleri doğru (Sent/Spam kendi içeriğini gösteriyor, INBOX değil).
- Sidebar Folders bölümü gerçek mailbox'ları gösteriyor.
- Inbox All/Unread/Starred sekmeleri doğru filtreliyor (66 / 63 / 0).
- Filtreler (Sieve) listeleme/oluşturma/aç-kapa/silme çalışıyor.
- Otomatik yanıt (Settings → Out of Office) kaydediliyor ve kalıcı.
- Trash'te kalıcı silme ve "Empty Trash" silme API'sini çağırıyor.
- Tema (açık/koyu) anahtarı çalışıyor.
- Klavye kısayolları penceresi header butonundan açılıyor.
- Search sorgusu backend'e ulaşıyor ve eşleşme sayısı dönüyor (ancak sonuç
  içeriği boş — aşağıya bakınız).

## Bulgular (hatalar ve eksikler)

### B1. Hatalı girişte hiçbir geri bildirim yok; sayfa sessizce yenileniyor
- Belirti: Yanlış parolayla giriş denemesinde hata mesajı görünmüyor, sayfa
  yeniden yükleniyor.
- Kanıt: `POST /api/v1/auth/login` → 401, ardından DOM'da "Invalid email or
  password" metni yok.
- Kök neden: `webmail/src/utils/api.ts` `request()` metodu (≈204-209) **tüm**
  401 yanıtlarında global olarak `window.location.href = '/login'` (sayfa
  yeniden yükleme) yapıp `null` döndürüyor, hata fırlatmıyor. Login endpoint'inin
  kendi 401'i de bunu tetiklediği için `login.tsx`'teki hata gösterimi
  (`setError('Invalid email or password')`) ölü kod kalıyor.
- İlgili dosya: `webmail/src/utils/api.ts`, `webmail/src/pages/login.tsx`.

### B2. Oturum kapatma (logout) çalışmıyor
- Belirti: Header'daki kullanıcı menüsünde "Sign Out" öğesine tıklamak hiçbir şey
  yapmıyor; kullanıcı oturumu kapatamıyor.
- Kanıt: "Sign Out" tıklandıktan sonra URL `/inbox` olarak kalıyor; `logout()`
  fonksiyonu `AuthContext.tsx` dışında hiçbir bileşen tarafından çağrılmıyor
  (grep yalnızca AuthContext tanımını buldu).
- İlgili dosya: `webmail/src/components/layout/header.tsx`,
  `webmail/src/contexts/AuthContext.tsx`.

### B3. Kullanıcı menüsü sabit (hardcoded) sahte kimlik gösteriyor
- Belirti: Avatar menüsü oturum açan kullanıcı yerine "User Name /
  user@example.com" gösteriyor.
- Kanıt: Giriş `qa.alice@local.test` iken menü `user@example.com` yazıyor.
- İlgili dosya: `webmail/src/components/layout/header.tsx`.

### B4. "Profile" ve "Account Settings" menü öğeleri işlevsiz
- Belirti: Avatar menüsündeki "Profile" ve "Account Settings" öğelerine tıklamak
  hiçbir sayfaya yönlendirmiyor (URL `/inbox`'ta kalıyor).
- İlgili dosya: `webmail/src/components/layout/header.tsx`.

### B5. SPA oturumu bellekte; sayfa yeniden yüklenince (veya herhangi bir 401'de) oturum düşüyor
- Belirti: Adres çubuğundan doğrudan bir sayfaya gitmek veya sayfayı yenilemek
  kullanıcıyı `/login`'e atıyor.
- Kök neden: Kimlik durumu `AuthContext` içinde yalnızca bellekte tutuluyor;
  yeniden yüklemede yeniden hidrasyon yok. B1'deki global 401 yönlendirmesi de
  aynı etkiyi üretiyor.
- İlgili dosya: `webmail/src/contexts/AuthContext.tsx`, `webmail/src/utils/api.ts`.

### B6. Inbox'taki toplu "Delete" gerçekten silmiyor
- Belirti: Inbox'ta mesaj seçip Delete'e basınca "moved to trash" toast'ı çıkıyor
  ama mesaj yeniden yüklemede geri geliyor.
- Kök neden: `inbox.tsx` `handleDelete` yalnızca yerel state'ten çıkarıyor
  (`setEmails(filter)`); `api.deleteMail` çağrısı yok. Dosyadaki tek API çağrısı
  listeyi yüklemek (`api.get('/mail/...')`).
- İlgili dosya: `webmail/src/pages/inbox.tsx`.

### B7. Inbox "Archive" işlevsiz
- Belirti: Archive butonu/dropdown öğesi yalnızca toast gösteriyor; mesaj listede
  bile kalıyor.
- Kök neden: `handleArchive` yalnızca toast (`toast.success`); arşivleme için
  backend endpoint'i de yok.
- İlgili dosya: `webmail/src/pages/inbox.tsx`.

### B8. Inbox toplu "Mark as read" ve satır "Mark as read" kalıcı değil
- Belirti: Toplu/satır "okundu işaretle" yalnızca yerel state'i değiştiriyor,
  yeniden yüklemede sıfırlanıyor.
- Kök neden: `handleMarkRead` ve satır `markAsRead` API çağırmıyor.
- İlgili dosya: `webmail/src/pages/inbox.tsx`.

### B9. Yıldız (star/flag) kalıcı değil
- Belirti: Inbox satırındaki yıldıza tıklamak yalnızca yerel görünümü
  değiştiriyor; yeniden yüklemede kayboluyor.
- Kök neden: `toggleStar` yerel state güncelliyor; backend'de mesajı flag'lemek
  için bir API endpoint'i yok.
- İlgili dosya: `webmail/src/pages/inbox.tsx`.

### B10. "Refresh" butonu gerçekten yenilemiyor
- Belirti: Inbox yenile butonu 1 saniye dönen ikon ve toast gösteriyor ama postayı
  yeniden çekmiyor.
- Kanıt/Kök neden: `handleRefresh` yalnızca `setTimeout(1000)` + `toast.success("
  inbox refreshed")` yapıyor; API çağrısı yok. (Ayrıca toast metninde baştaki
  fazla boşluk: `" inbox refreshed"`.)
- İlgili dosya: `webmail/src/pages/inbox.tsx`.

### B11. Trash "Restore" aslında mesajı kalıcı siliyor
- Belirti: Trash'te "Restore" butonu mesajı geri taşımıyor; siliyor.
- Kök neden: `handleRestore`, `api.delete('/mail/delete?id=')` (kalıcı silme)
  çağırıyor ama toast "Message restored" diyor. Backend'de mesajı başka klasöre
  taşıyan bir API yok.
- İlgili dosya: `webmail/src/pages/trash.tsx`.

### B12. "Çöpe taşıma" yok; silme her zaman kalıcı; Trash klasörü hep boş
- Belirti: Hiçbir silme işlemi mesajı Trash mailbox'ına taşımıyor; `handleMailDelete`
  mesaj dosyasını ve metadata'sını tamamen siliyor. Sonuç: Trash görünümü pratikte
  hep boş, "moved to trash" ifadeleri yanıltıcı.
- İlgili dosya: `internal/api/mail.go` (`handleMailDelete`, `deleteMessageMetadata`).

### B13. Search sonuçları boş içerikle dönüyor (kullanılamaz)
- Belirti: "mail-flow" araması "20 result" diyor ama her satır "—" (boş) görünüyor.
- Kanıt: `GET /api/v1/search?q=mail-flow` → 200, ancak her öğede `from`, `to`,
  `subject`, `preview`, `date` alanları **boş string**; kimlik alanı `item_id`
  (istemci `id` okuyor). Yanıt örneği: `{"item_id":"...","from":"","subject":"",
  "preview":"","date":"",...}`.
- Kök neden: Arama indeksi yalnızca `item_id`/`conversation_id`/`score` döndürüyor;
  görüntülenebilir mesaj metadata'sı doldurulmuyor ve istemcinin beklediği alan
  adları (`id`, `from`, `subject`) ile uyuşmuyor. Sonuçlar boş ve doğru mesaja
  tıklanamaz.
- İlgili dosya: `internal/api/` arama handler'ı, `webmail/src/pages/search.tsx`,
  `webmail/src/utils/api.ts` (`SearchResponse`).

### B14. Kişi oluşturma kalıcı olmuyor
- Belirti: Contacts'ta yeni kişi eklenince başarı görünüyor ama kişi listede hiç
  görünmüyor; yeniden yüklemede de yok.
- Kanıt: `POST /api/v1/contacts` → 200, ancak sonraki `GET /api/v1/contacts` →
  `{"contacts":[],"total":0}`.
- Kök neden: Oluşturma 200 dönüyor fakat oluşturulan kişi okunan depodan
  dönmüyor (yazma/okuma deposu uyumsuzluğu olası).
- İlgili dosya: `internal/api/` contacts handler'ı, `webmail/src/pages/contacts.tsx`.

### B15. Composer'a alıcı eklenemiyor — e-posta gönderilemiyor
- Belirti: Compose'da "To" alanına serbest e-posta adresi yazılamıyor; yalnızca
  kayıtlı kişiler arasından seçilebiliyor. Kişi de oluşturulamadığı (B14) ve hiç
  kişi olmadığı için hiçbir alıcı eklenemiyor, Send butonu disabled kalıyor.
- Kanıt: "qa.bob@local.test" yazıp Enter'a basmak alıcı eklemiyor; Send disabled
  kalıyor. `addRecipient` yalnızca `filteredContacts.map(...)` üzerindeki `onClick`
  ile çağrılıyor; serbest metin/e-posta ekleme yolu yok.
- Sonuç: Webmail composer'ından hiç e-posta gönderilemiyor (çekirdek işlev
  kullanılamaz).
- İlgili dosya: `webmail/src/pages/compose.tsx`.

### B16. "Edit draft" boş composer açıyor (taslak yüklenmiyor)
- Belirti: Drafts'ta bir taslağı düzenlemek için tıklayınca `/compose?draft=<id>`
  açılıyor ama composer boş.
- Kök neden: `compose.tsx` `?draft=` parametresini hiç okumuyor; taslağı yükleyen
  kod yok.
- İlgili dosya: `webmail/src/pages/compose.tsx`, `webmail/src/pages/drafts.tsx`.

### B17. "Save draft" taslağı kaydetmiyor (kalıcı değil)
- Belirti: Composer'da "Save draft" başarı toast'ı gösteriyor ama Drafts klasörü
  webmail'den hiç dolmuyor.
- Kök neden: `handleSaveDraft` → `handleAutoSave`, yalnızca yerel `setLastSaved`
  yapıyor; taslak kaydetmek için istemcide veya backend'de API yok.
- İlgili dosya: `webmail/src/pages/compose.tsx`.

### B18. Composer biçimlendirme araç çubuğu işlevsiz
- Belirti: Bold/Italic/Underline/link/liste/görsel butonlarına tıklamak hiçbir şey
  yapmıyor.
- Kök neden: Bu butonların `onClick` handler'ı yok (yalnızca `title` öznitelikleri).
- İlgili dosya: `webmail/src/pages/compose.tsx`.

### B19. Ekler (attachments) gönderilmiyor
- Belirti: Composer'da dosya eklenebiliyor (yerel state) ama gönderimde sessizce
  kayboluyor.
- Kök neden: `SendMailRequest` / `handleMailSend` ek desteği içermiyor; eklenen
  dosyalar yalnızca yerel state'te tutuluyor.
- İlgili dosya: `webmail/src/pages/compose.tsx`, `internal/api/mail.go`.

### B20. Bildirim çanı sahte
- Belirti: Header'daki çan sabit "3" rozeti ve sabit 3 sahte bildirim ("New email
  / From: test@example.com / i minutes ago") gösteriyor.
- Kök neden: `header.tsx`'te `[1,2,3].map(...)` ile hardcoded içerik; gerçek
  veriye bağlı değil.
- İlgili dosya: `webmail/src/components/layout/header.tsx`.

### B21. Settings'teki bildirim/gizlilik/yazım anahtarları kalıcı değil
- Belirti: Notifications, Email Composition, Privacy & Security bölümlerindeki
  anahtarlar toast gösteriyor ama hiçbir yere kaydedilmiyor (yeniden yüklemede
  sıfırlanıyor).
- Kök neden: `handleToggle` yalnızca yerel state + toast; bu ayarlar için backend
  yok. (Yalnızca otomatik yanıt gerçek API kullanıyor.)
- İlgili dosya: `webmail/src/pages/settings.tsx`.

### B22. Threads/konuşma görünümü arayüzü yok
- Belirti: `api.getThreads` / `api.getThread` ve backend `/api/v1/threads*`
  endpoint'leri var ama hiçbir webmail sayfası/route'u bunları kullanmıyor.
- İlgili dosya: `webmail/src/` (eksik), `internal/api/` (threads handler mevcut).

### B23. Push bildirimi arayüzü yok / abonelik yapılmıyor
- Belirti: `api.subscribePush` / `getVapidPublicKey` ve backend push endpoint'leri
  var ama webmail hiçbir yerde service worker kaydı yapmıyor veya push aboneliği
  oluşturmuyor. Settings'teki "Browser/Desktop notifications" anahtarları yalnızca
  kozmetik (B21).
- İlgili dosya: `webmail/src/` (eksik), `internal/api/` (push handler mevcut).

### B24. (GEÇERSİZ — ikinci turda düzeltildi) "?" kısayolu çalışıyor
- İlk turda `?` kısayolunun pencereyi açmadığı bildirilmişti. İkinci turda doğru
  modifier ile (Shift+/) yeniden test edildi: kısayol penceresi açılıyor. Ayrıca
  Cmd/Ctrl+1..4 navigasyon kısayolları da çalışıyor (örneğin Ctrl+2 → /sent).
  İlk testte modifier'sız sentetik `?` tuşu gönderildiği için yanlış pozitif
  oluşmuştu. Bu madde geçersizdir.

## İkinci tur — ek bulgular (B25–B33)

### B25. Starred görünümü her zaman boş ve yanlış boş-durum metni gösteriyor
- Belirti: `/starred` her zaman boş; ayrıca boş-durum metni "Your inbox is empty."
  diyor (oysa Starred görünümündeyiz).
- Kök neden: `inbox.tsx` `folder === "starred"` iken INBOX'u yükleyip
  `e.starred`'a göre filtreliyor; ancak yıldız hiç kalıcı olmadığı (B9) ve hiçbir
  mesajda `\Flagged` bayrağı set edilmediği için filtre her zaman boş dönüyor.
  Boş-durum metni de `folder` yerine sekme (`activeFilter`) değerine bakıyor;
  `activeFilter` "all" olduğundan "Your inbox is empty." gösteriliyor.
- İlgili dosya: `webmail/src/pages/inbox.tsx` (≈74, 101, 407).

### B26. logout() mevcut /api/v1/auth/logout endpoint'ini çağırmıyor (oturum çerezi sunucuda temizlenmiyor)
- Belirti/Kök neden: Backend'de `POST /api/v1/auth/logout` (`handleLogout`,
  server.go:567) var, ancak webmail `AuthContext.logout()` yalnızca
  `api.setToken(null)` yapıyor; logout endpoint'ini hiç çağırmıyor. Dolayısıyla
  Sign Out wired olsa bile (bkz. B2) HttpOnly oturum çerezi sunucu tarafında
  geçersiz kılınmayacaktı.
- İlgili dosya: `webmail/src/contexts/AuthContext.tsx`, `webmail/src/utils/api.ts`.

### B27. Sayfalama (pagination) sahte — ileri/geri butonları her zaman pasif
- Belirti: Inbox/Sent/Drafts gibi listelerin altındaki önceki/sonraki butonları
  her durumda `disabled`; gerçek sayfalama yok. Tüm mesajlar tek seferde
  yükleniyor (örn. 66 mesaj), büyük kutularda performans/ölçeklenme sorunu.
- Kök neden: Butonlar koşulsuz `disabled` olarak sabitlenmiş; sayfa state'i yok.
- İlgili dosya: `webmail/src/pages/inbox.tsx` (≈424-428), `sent.tsx`, `drafts.tsx`.

### B28. Reply All, Move to folder ve Mark as unread aksiyonları yok
- Belirti: Webmail'de "Reply All", "Move to folder" ve "Mark as unread" gibi temel
  e-posta aksiyonları hiçbir yerde yok (yalnızca tekli Reply, Forward, Delete var).
- İlgili dosya: `webmail/src/pages/email-detail.tsx`, `inbox.tsx`.

### B29. Filtre sıralaması/önceliği için arayüz yok
- Belirti: Backend `POST /api/v1/filters/reorder` ve filtre `priority` alanı var,
  ancak Filters sayfasında filtreleri yeniden sıralama/öncelik düzenleme arayüzü
  yok.
- İlgili dosya: `webmail/src/pages/filters.tsx`, `internal/api/filters.go`.

### B30. Aktif oturum yönetimi (sessions) arayüzü yok
- Belirti: Backend `/api/v1/sessions` (listeleme) ve `/api/v1/sessions/` (iptal)
  endpoint'leri var, ancak webmail'de aktif oturumları görüntüleme/iptal etme
  arayüzü yok.
- İlgili dosya: `webmail/src/` (eksik), `internal/api/server.go` (sessions handler
  mevcut).

### B31. Mobil menü butonu işlevsiz
- Belirti: Header'daki hamburger (mobil menü) butonu çalışmıyor; küçük ekranda
  sidebar açılamıyor.
- Kök neden: `layout.tsx` `onMenuToggle` ile `mobileMenuOpen` state'ini değiştiriyor
  ama bu state hiçbir yerde kullanılmıyor (Sidebar'a geçirilmiyor); ana yerleşim
  de duyarlı (responsive) değil, her zaman masaüstü padding'i (`pl-64`/`pl-16`)
  uyguluyor.
- İlgili dosya: `webmail/src/components/layout/layout.tsx`,
  `webmail/src/components/layout/header.tsx`.

### B32. Settings "Manage Account" butonu işlevsiz
- Belirti: Settings → Account Security'deki "Manage Account" butonuna tıklamak
  hiçbir şey yapmıyor.
- Kök neden: Butonun `onClick` handler'ı yok (settings.tsx:442). Webmail içinde
  hesap/parola yönetimi arayüzü yok.
- İlgili dosya: `webmail/src/pages/settings.tsx`.

### B33. Dialog'larda erişilebilirlik uyarısı (eksik açıklama/aria-describedby)
- Belirti: Konsolda her dialog açılışında "Missing `Description` or
  `aria-describedby={undefined}` for {DialogContent}" uyarısı çıkıyor.
- Kök neden: Radix `DialogContent` bileşenleri (compose alıcı seçici, filtre
  düzenleme, kişi ekleme, kısayollar vb.) `DialogDescription`/`aria-describedby`
  içermiyor; ekran okuyucu erişilebilirliği eksik.
- İlgili dosya: dialog kullanan bileşenler (`filters.tsx`, `contacts.tsx`,
  `compose.tsx`, `shortcuts-dialog.tsx`).

### B34. Inbox sıralamasında "Date" seçeneği işlevsiz, sıralama yönü kontrolü yok
- Belirti: Inbox sıralama menüsünde "Date" seçildiğinde liste tarihe göre
  sıralanmıyor; ayrıca artan/azalan (asc/desc) yön seçimi hiç yok.
- Kök neden: Sıralama comparator'ı `sortBy === "date"` durumunda koşulsuz
  `return 0` yapıyor (inbox.tsx:186, "Keep original order for date"), yani gerçek
  bir tarih karşılaştırması yapılmıyor. "date" varsayılan sıralama olduğu için
  (inbox.tsx:65) kullanıcı önce Sender/Subject'e göre sıraladıktan sonra tekrar
  "Date" seçtiğinde liste tarih sırasına dönmez; menüde "Date ✓" göstermesine
  rağmen hiçbir şey değişmez. Sender ve Subject sıralaması (`localeCompare`)
  çalışıyor. Sıralama yalnızca o anda yüklü listede istemci tarafında yapılıyor;
  sunucu tarafı sıralama yok.
- İlgili dosya: `webmail/src/pages/inbox.tsx` (≈65, 185-190, 345-358).

## Özet

Mevcut webmail, okuma odaklı akışlar (postaları listeleme, bir mesajı okuma,
yanıtlama/iletme/silme detay görünümünden, klasör görünümleri, filtreler, otomatik
yanıt) büyük ölçüde çalışıyor. Ancak yazma ve yönetim akışlarının önemli bir
bölümü ya sahte (yalnızca toast/yerel state) ya da uçtan uca kırık:

- E-posta gönderme uçtan uca kullanılamıyor (B15), taslaklar kaydedilmiyor/
  yüklenmiyor (B16, B17), ekler gönderilmiyor (B19).
- Inbox'taki toplu/satır aksiyonları (Delete, Archive, Mark read, Star) kalıcı
  değil (B6–B9), Refresh gerçek değil (B10).
- Search sonuçları boş (B13), kişi oluşturma kalıcı değil (B14).
- Oturum kapatma yok (B2), hatalı girişte geri bildirim yok (B1), kullanıcı menüsü
  ve bildirimler sahte (B3, B4, B20).
- Çöpe taşıma yok/silme kalıcı (B11, B12), Threads ve Push için arayüz yok
  (B22, B23).

İkinci tur ek bulgular: Starred görünümü her zaman boş ve yanlış metinli (B25),
logout sunucu çerezini temizlemiyor (B26), sayfalama sahte (B27), Reply All/Move/
Mark-unread yok (B28), filtre sıralama arayüzü yok (B29), oturum yönetimi arayüzü
yok (B30), mobil menü butonu işlevsiz (B31), "Manage Account" işlevsiz (B32),
dialog'larda erişilebilirlik uyarısı (B33), inbox "Date" sıralaması işlevsiz ve
sıralama yönü kontrolü yok (B34). İlk turdaki B24 (klavye kısayolu) yeniden test
edilip geçersiz olarak işaretlendi — kısayollar çalışıyor.

Compose Cc/Bcc gizle/göster ve gönderen kimliği (send-as / on-behalf) açılır
menüsü çalışıyor; alıcı ekleme kısıtı (serbest e-posta girişi yok) to/cc/bcc'nin
hepsinde geçerli ve B15 kapsamındadır. Backend `handleMailSend` cc/bcc/from
alanlarını kabul ediyor, ancak `SendMailRequest` tipinde ek (attachment) alanı yok
(B19 ile tutarlı). Inbox liste/kompakt görünüm modu çalışıyor.

Toplam: B1–B34 arası 33 geçerli bulgu (B24 geçersiz). Çekirdek sorun değişmedi:
okuma akışları çalışıyor, ancak gönderme/taslak/ek, inbox toplu aksiyonları,
arama sonuçları, kişiler, oturum kapatma ve birçok yönetim akışı sahte ya da
uçtan uca kırık.

## Çözüm durumu

Geçerli 33 bulgunun tamamı (B1–B23, B25–B34) ayrı atomik commit'lerle düzeltildi;
B24 zaten geçersizdi. Her düzeltme için frontend (`npm run typecheck/lint/test/
build`) ve değişen backend için `make test`/`make lint` çalıştırıldı; faz
sınırlarında Docker yığını yeniden derlenip canlı smoke testlerinden geçirildi.

Öne çıkan kalıcı değişiklikler:

- Kimlik/oturum: 401 ayrımı (B1), `/auth/logout` çağrısı (B26), Sign Out (B2),
  gerçek kullanıcı menüsü (B3), Profile/Account Settings yönlendirmesi (B4),
  sayfa yenilemede oturum hidrasyonu için yeni `GET /api/v1/auth/me` (B5).
- Mail aksiyonları (yeni backend endpoint'leri): silme artık Trash'e taşıyor
  (`handleMailDelete`, B12), `POST /mail/flag` ile kalıcı okundu/yıldız (B8, B9),
  `POST /mail/move` ile geri yükleme/arşivleme/taşıma (B11, B7, B28), inbox
  Delete/Refresh/sıralama/sayfalama (B6, B10, B34, B27), Starred metni (B25).
- Compose: serbest alıcı girişi (B15), `POST /mail/draft` ile taslak kaydet/
  yükle (B17, B16), biçimlendirme araç çubuğu (B18), `multipart/mixed` ile ek
  gönderimi (B19), Reply All/Cc parametreleri (B28).
- Arama doğrudan mailbox taramasına çevrildi, gerçek çözülebilir id'ler (B13).
- Kişiler: create/update route'landı ve `default` adres defteri listelenir
  oldu (B14).
- Yönetim/UI: gerçek bildirim çanı (B20), kalıcı ayar anahtarları için
  `GET/PUT /api/v1/preferences` (B21), konuşma görünümü (B22), web push +
  service worker (B23), filtre sıralama (B29), oturum yönetimi arayüzü (B30),
  mobil menü (B31), self-service parola değişikliği `POST /api/v1/account/
  password` (B32), dialog erişilebilirlik açıklamaları (B33).
