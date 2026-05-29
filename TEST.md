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
