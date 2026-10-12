# Server Monitor (Go)

Agent yang berjalan di setiap server untuk memantau CPU, RAM, kapasitas disk, dan (opsional) replikasi MySQL. Mengirim alert ketika nilai **lebih besar** dari batas, notifikasi ketika pulih, dan laporan snapshot harian pukul **06.00 Asia/Makassar (WITA)** ke Telegram. Zona waktu dan jadwal dapat diubah.

## Fitur dan perilaku

- CPU: persentase agregat semua core, sampel 1 detik secara default.
- RAM: `(total - available) / total`, sehingga cache Linux yang tersedia tidak dianggap habis terpakai.
- Disk: penggunaan kapasitas filesystem untuk setiap path di `DISK_PATHS`; bukan metrik I/O. Default hanya `/`; tambahkan mount data secara eksplisit.
- MySQL: default **OFF**, diaktifkan lewat `MYSQL_ENABLED=true`. Memantau seluruh channel yang dikembalikan server, termasuk lag dan status thread IO/SQL.
- Mendukung `SHOW REPLICA STATUS` / `Seconds_Behind_Source`, dengan fallback `SHOW SLAVE STATUS` / `Seconds_Behind_Master` jika syntax belum didukung.
- Lag `NULL`, thread tidak `Yes`, status kosong, kegagalan query, dan kegagalan membaca metrik dianggap masalah dan menghasilkan alert.
- Alert pertama langsung dikirim pada pemeriksaan yang mendeteksi masalah. Pengulangan setiap `ALERT_COOLDOWN`, per CPU/RAM/path disk; MySQL digabung menjadi satu status untuk semua channel. Pemulihan dikirim setelah pemeriksaan kembali normal.
- Kegagalan Telegram dicatat ke journal. Alert gagal dicoba pada pemeriksaan berikutnya jika kondisi masih memerlukan notifikasi; laporan harian gagal dicoba setiap menit dengan snapshot terbaru.
- Timeout HTTP/MySQL, penghentian dengan SIGTERM/SIGINT, dan token/password tidak ditulis ke log aplikasi.

Laporan harian adalah **snapshot saat pengambilan**, bukan rata-rata atau histori 24 jam. Agent tidak membuka port HTTP dan tidak menyimpan histori. Saat mulai, agent langsung memeriksa threshold; laporan pertama mengikuti jadwal berikutnya. Tidak ada catch-up laporan yang terlewat ketika service mati. State cooldown disimpan di memori, sehingga restart dapat menghasilkan alert ulang. Pengambilan CPU serta pengiriman notifikasi memerlukan waktu; laporan dikirim sesaat setelah jadwal dan dapat tertunda jika pemeriksaan/pengiriman sebelumnya masih berlangsung. Pesan panjang dibagi beberapa bagian; retry setelah pengiriman sebagian atau timeout dapat menghasilkan duplikat.

## Menjalankan lokal

Butuh Go 1.23+ saat build, akses HTTPS ke `api.telegram.org`, dan Linux dengan systemd untuk deployment berikut. Binary menyertakan data zona waktu.

```bash
cp .env.example .env
chmod 600 .env
# Edit .env: minimal TELEGRAM_BOT_TOKEN dan TELEGRAM_CHAT_ID
go mod download
go build -o bin/service-health-monitor ./cmd/monitor
./bin/service-health-monitor -env .env
```

Tes pengiriman satu laporan lalu keluar:

```bash
./bin/service-health-monitor -env .env -once
```

`-once` mengirim snapshot walaupun metrik bermasalah; exit code 0 berarti pengiriman berhasil, bukan seluruh server sehat. Exit nonzero untuk konfigurasi atau pengiriman gagal. Jangan jalankan `-once` tanpa kredensial yang benar jika ingin menguji pengiriman nyata.

File `.env` dimuat oleh aplikasi; variabel environment proses mendapat prioritas. File default `.env` boleh tidak ada bila konfigurasi diberikan dari environment. Path eksplisit yang tidak dapat dibaca akan menyebabkan startup gagal. Nilai invalid ditolak saat startup. Gunakan kutip untuk password dengan spasi atau `#` sesuai format dotenv. `.env` diabaikan Git.

## Konfigurasi

| Variabel | Default | Keterangan |
| --- | --- | --- |
| `SERVER_NAME` | hostname OS | Nama pada pesan |
| `CHECK_INTERVAL` | `60s` | Interval pemeriksaan |
| `CPU_SAMPLE_DURATION` | `1s` | Durasi sampel; wajib lebih kecil dari interval |
| `CPU_THRESHOLD` | `80` | Persen, > 0 sampai 100 |
| `RAM_THRESHOLD` | `80` | Persen, > 0 sampai 100 |
| `DISK_THRESHOLD` | `85` | Persen, > 0 sampai 100 |
| `DISK_PATHS` | `/` | Path absolut dipisahkan koma, contoh `/,/var/lib/mysql,/data` |
| `ALERT_COOLDOWN` | `30m` | Jeda pengulangan alert aktif |
| `REPORT_TIME` | `06:00` | Format 24 jam `HH:MM` |
| `TIMEZONE` | `Asia/Makassar` | Misalnya `Asia/Jakarta` untuk WIB |
| `TELEGRAM_BOT_TOKEN` | wajib | Token dari BotFather |
| `TELEGRAM_CHAT_ID` | wajib | ID chat tujuan; grup biasanya bernilai negatif |
| `TELEGRAM_TIMEOUT` | `15s` | Timeout per permintaan |
| `MYSQL_ENABLED` | `false` | `true` / `false`; saat off tidak membuka koneksi MySQL |
| `MYSQL_HOST` | `127.0.0.1` | Alamat **replica**, bukan primary |
| `MYSQL_PORT` | `3306` | Port TCP |
| `MYSQL_USER` | `server_monitor` | User pemantau |
| `MYSQL_PASSWORD` | kosong | Password MySQL |
| `MYSQL_LAG_THRESHOLD` | `60` | Batas detik; 0 berarti setiap lag positif memicu alert |
| `MYSQL_TIMEOUT` | `5s` | Timeout koneksi dan query |
| `MYSQL_TLS` | `false` | `true` menggunakan TLS dengan verifikasi sertifikat sistem |

Semua durasi harus positif, contoh `5s`, `1m`, `1h`. Konfigurasi MySQL diabaikan ketika fitur off. Untuk sertifikat CA privat, pasang CA tersebut ke trust store sistem sebelum menggunakan TLS.

## Telegram

1. Buat bot melalui `@BotFather` dan simpan token di `.env`.
2. Buka chat bot lalu kirim `/start`. Untuk grup, tambahkan bot dan pastikan ia boleh mengirim pesan.
3. Dapatkan ID chat dari `message.chat.id` pada respons metode `getUpdates` Bot API setelah mengirim pesan ke bot. Simpan ke `TELEGRAM_CHAT_ID`. `getUpdates` membutuhkan bot tanpa webhook aktif.
4. Jalankan mode `-once` untuk memverifikasi chat dan token.

Dokumentasi resmi: [Telegram Bot API](https://core.telegram.org/bots/api#sendmessage).

## Mengaktifkan monitor MySQL

Buat user pada **replica**. Contoh untuk agent yang terhubung melalui TCP localhost; sesuaikan host akun dengan asal koneksi agent:

```sql
CREATE USER 'server_monitor'@'127.0.0.1' IDENTIFIED BY 'GANTI_PASSWORD_KUAT';
GRANT REPLICATION CLIENT ON *.* TO 'server_monitor'@'127.0.0.1';
```

Kemudian ubah `.env`:

```dotenv
MYSQL_ENABLED=true
MYSQL_HOST=127.0.0.1
MYSQL_USER=server_monitor
MYSQL_PASSWORD='GANTI_PASSWORD_KUAT'
MYSQL_LAG_THRESHOLD=60
```

Setelah perubahan, restart service. Untuk menonaktifkan cukup set `MYSQL_ENABLED=false` lalu restart.

`Seconds_Behind_Source/Master` merupakan indikator keterlambatan replikasi, bukan jaminan bahwa replica memiliki semua transaksi terbaru. Karena itu agent juga memeriksa kedua thread replikasi. Referensi: [MySQL SHOW REPLICA STATUS](https://dev.mysql.com/doc/refman/8.0/en/show-replica-status.html).

## Deployment Linux dengan systemctl

Jalankan dari root direktori proyek **pada server Linux target**, menggunakan akun dengan sudo. Build sesuai arsitektur server (jangan menyalin binary macOS ke Linux).

```bash
go mod download
go build -trimpath -o bin/service-health-monitor ./cmd/monitor

# Buat service account satu kali, jika belum ada.
id server-monitor >/dev/null 2>&1 || sudo useradd --system --user-group --no-create-home --shell /usr/sbin/nologin server-monitor

sudo install -d -o root -g root -m 755 /opt/server-monitor/bin
sudo install -m 755 bin/service-health-monitor /opt/server-monitor/bin/service-health-monitor
sudo install -d -o root -g server-monitor -m 750 /etc/server-monitor

# Hanya saat instalasi pertama; jangan timpa konfigurasi produksi saat update.
sudo install -o root -g server-monitor -m 640 .env.example /etc/server-monitor/server-monitor.env
sudoedit /etc/server-monitor/server-monitor.env

sudo install -m 644 deploy/server-monitor.service /etc/systemd/system/server-monitor.service
sudo systemctl daemon-reload

# Opsional: kirim laporan uji menggunakan permission service account.
sudo -u server-monitor /opt/server-monitor/bin/service-health-monitor -env /etc/server-monitor/server-monitor.env -once

sudo systemctl enable --now server-monitor
sudo systemctl status server-monitor --no-pager
sudo journalctl -u server-monitor -f
```

Unit memakai user non-root dan membaca file konfigurasi langsung lewat `-env`. User tersebut harus mempunyai izin menelusuri path filesystem yang dipantau. Bila path tidak dapat dibaca, agent akan mengirim alert kesalahan metrik. Gunakan lokasi mount yang benar dan pastikan mount tersedia sebelum memulai service.

Operasional:

```bash
# Terapkan perubahan .env
sudo systemctl restart server-monitor

# Log terakhir
sudo journalctl -u server-monitor -n 100 --no-pager

# Hentikan / matikan autostart
sudo systemctl stop server-monitor
sudo systemctl disable server-monitor
```

Update binary tanpa menimpa konfigurasi:

```bash
go build -trimpath -o bin/service-health-monitor ./cmd/monitor
sudo systemctl stop server-monitor
sudo install -m 755 bin/service-health-monitor /opt/server-monitor/bin/service-health-monitor
sudo systemctl start server-monitor
sudo systemctl status server-monitor --no-pager
```

Jika build dilakukan di macOS untuk Linux, contoh target x86_64:

```bash
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -o bin/service-health-monitor-linux-amd64 ./cmd/monitor
# Untuk ARM64 ganti GOARCH=arm64 dan nama output.
```

Salin binary yang sesuai ke server dan gunakan sebagai sumber pada perintah `sudo install` di atas. Server target memerlukan CA certificates sistem untuk HTTPS Telegram dan TLS MySQL.

## Build dan release otomatis dengan GitHub Actions

Workflow [release.yml](.github/workflows/release.yml) berjalan setiap kali tag dengan awalan `v` dipush, misalnya `v1.0.0`. Workflow memverifikasi dependency, menjalankan `go test -race` dan `go vet`, lalu membangun binary Linux dengan CGO dinonaktifkan. Release hanya dibuat jika semua pemeriksaan dan build berhasil.

Commit dan push source beserta workflow terlebih dahulu, kemudian buat tag pada commit yang ingin dirilis:

```bash
git tag -a v1.0.0 -m "Release v1.0.0"
git push origin v1.0.0
```

Pantau tab **Actions** pada repository. Setelah selesai, halaman **Releases → v1.0.0** akan berisi release notes otomatis dan tiga asset:

- `service-health-monitor-linux-amd64` untuk server x86_64.
- `service-health-monitor-linux-arm64` untuk server ARM64/aarch64.
- `checksums.txt` untuk verifikasi SHA256 kedua binary.

Workflow memakai `GITHUB_TOKEN` bawaan dengan permission `contents: write`; tidak perlu membuat secret tambahan. GitHub Actions harus aktif dan kebijakan repository/organisasi harus mengizinkan permission tersebut. Push branch biasa tidak memicu release. Semua tag `v*`, termasuk tag seperti `v1.0.0-rc1`, menghasilkan release biasa. Gunakan versi baru untuk setiap rilis; rerun workflow pada tag yang sama akan memperbarui asset release yang sudah ada.

Unduh binary sesuai arsitektur server dan `checksums.txt` dari release yang sama. Verifikasi lalu pasang sebagai binary service:

```bash
# Jalankan di direktori hasil unduhan; --ignore-missing melewati arsitektur yang tidak diunduh.
sha256sum --check --ignore-missing checksums.txt

# Contoh update server x86_64; ganti amd64 menjadi arm64 bila diperlukan.
sudo systemctl stop server-monitor
sudo install -m 755 service-health-monitor-linux-amd64 /opt/server-monitor/bin/service-health-monitor
sudo systemctl start server-monitor
sudo systemctl status server-monitor --no-pager
```

Untuk instalasi pertama, ikuti bagian deployment systemctl, menggunakan binary release sebagai pengganti langkah build. File `.env` tetap dikonfigurasi pada server dan tidak dimasukkan ke release. Publikasi menggunakan [GitHub CLI release](https://cli.github.com/manual/gh_release_create).

## Pengujian

```bash
go test -race ./...
go vet ./...
```

Pengujian mencakup validasi konfigurasi, pergantian tanggal laporan, status replikasi lama/baru, threshold, cooldown, pemulihan, retry setelah pengiriman gagal, respons Telegram, dan pembagian pesan Unicode. Test Telegram menggunakan server HTTP lokal sehingga tidak mengirim pesan nyata. Koneksi replica dan bot produksi perlu diverifikasi dengan `-once` pada lingkungan deployment.
# server-monitor
