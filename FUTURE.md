# uMailServer Future Work

## Açık Alanlar

| Alan | Öncelik | Dosya/Konum | Açıklama |
|------|---------|-------------|----------|
| IMAP SCORE Extension | Orta | `internal/imap/sort.go` | `SCORE` sort criterion henüz desteklenmiyor |

## IMAP Geliştirmeleri

- [ ] `SCORE` extension desteği ekle
- [ ] Threading tarafındaki `NOTREVEALED` placeholder kullanımını gözden geçir

## Monitoring ve Alerting

- [ ] Email / Discord / Telegram için alert rule desteği ekle
- [ ] Backup success / failure alerting ekle
- [ ] Disk space ve queue depth için alerting ekle