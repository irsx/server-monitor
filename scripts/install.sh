#!/usr/bin/env bash
set -euo pipefail
umask 077

usage() {
  cat <<'EOF'
Install/update Server Monitor dari GitHub Release (Linux + systemd).

Usage: sudo bash install.sh [options]
  --version TAG       Versi release, contoh v1.0.0 (default: latest)
  --repo OWNER/REPO   Repository publik (default: irsx/server-monitor)
  --env-file PATH     Konfigurasi untuk instalasi pertama; konfigurasi lama dijaga
  --no-start          Pasang file saja, tanpa enable/start/restart service
  -h, --help          Tampilkan bantuan

Tanpa --env-file, instalasi pertama meminta token/chat ID jika terminal interaktif.
Tanpa terminal, template .env dibuat dan service belum diaktifkan.
EOF
}

die() { printf 'Error: %s\n' "$*" >&2; exit 1; }
repo=irsx/server-monitor
version=latest
env_source=
start_service=true
while (($#)); do
  case "$1" in
    --version|--repo|--env-file)
      (($# >= 2)) && [[ -n "$2" ]] || die "$1 membutuhkan nilai"
      case "$1" in
        --version) version=$2 ;;
        --repo) repo=$2 ;;
        --env-file) env_source=$2 ;;
      esac
      shift 2
      ;;
    --no-start) start_service=false; shift ;;
    -h|--help) usage; exit 0 ;;
    *) die "Opsi tidak dikenal: $1" ;;
  esac
done

[[ "$repo" =~ ^[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+$ ]] || die "Format repo harus OWNER/REPO"
[[ "$version" == latest || "$version" =~ ^v[A-Za-z0-9._-]+$ ]] || die "Versi harus tag v*, contoh v1.0.0"
[[ $(uname -s) == Linux ]] || die "Installer hanya mendukung Linux"
[[ $(id -u) == 0 ]] || die "Jalankan dengan sudo atau sebagai root"
[[ -d /run/systemd/system ]] || die "systemd harus berjalan pada server ini"
for cmd in curl sha256sum awk install mktemp systemctl getent useradd groupadd mv; do
  command -v "$cmd" >/dev/null 2>&1 || die "Command $cmd tidak tersedia; pasang dependency terlebih dahulu"
done
case $(uname -m) in
  x86_64|amd64) arch=amd64 ;;
  aarch64|arm64) arch=arm64 ;;
  *) die "Arsitektur tidak didukung: $(uname -m)" ;;
esac
[[ -z "$env_source" || -f "$env_source" && -r "$env_source" ]] || die "File --env-file tidak dapat dibaca"

config=/etc/server-monitor/server-monitor.env
binary=/opt/server-monitor/bin/service-health-monitor
unit=/etc/systemd/system/server-monitor.service
tmp_dir=$(mktemp -d)
trap 'rm -rf -- "$tmp_dir"' EXIT
curl_args=(--fail --silent --show-error --location --retry 3 --connect-timeout 15 --max-time 180 --proto '=https' --proto-redir '=https')

if [[ "$version" == latest ]]; then
  # Resolve once so every asset comes from the same release even during publication.
  release_url=$(curl "${curl_args[@]}" --output /dev/null --write-out '%{url_effective}' "https://github.com/$repo/releases/latest")
  [[ "$release_url" == "https://github.com/$repo/releases/tag/"* ]] || die "Gagal menemukan latest release"
  version=${release_url##*/}
  [[ "$version" =~ ^v[A-Za-z0-9._-]+$ ]] || die "Tag latest release tidak didukung: $version"
fi
base_url="https://github.com/$repo/releases/download/$version"
asset="service-health-monitor-linux-$arch"
printf 'Mengunduh %s dari %s (%s)...\n' "$version" "$repo" "$arch"
for name in "$asset" server-monitor.env.example server-monitor.service checksums.txt; do
  curl "${curl_args[@]}" --output "$tmp_dir/$name" "$base_url/$name" || die "Gagal mengunduh $name; pastikan release menyertakan asset installer"
done

# Only accept exactly one checksum for each expected file; never trust paths in a manifest.
for name in "$asset" server-monitor.env.example server-monitor.service; do
  expected=$(awk -v name="$name" '$2 == name { count++; sum=$1 } END { if (count == 1) print sum; else exit 1 }' "$tmp_dir/checksums.txt") || die "Checksum $name tidak ditemukan/duplikat"
  [[ "$expected" =~ ^[A-Fa-f0-9]{64}$ ]] || die "Checksum $name tidak valid"
  printf '%s  %s\n' "$expected" "$name" > "$tmp_dir/verify.txt"
  (cd "$tmp_dir" && sha256sum --check --status verify.txt) || die "Verifikasi checksum gagal: $name"
done
printf 'Checksum semua file valid.\n'

config_ready=true
if [[ -e "$config" ]]; then
  printf 'Mempertahankan konfigurasi: %s\n' "$config"
  if [[ -n "$env_source" ]]; then
    printf 'Opsi --env-file diabaikan karena konfigurasi sudah ada.\n'
  fi
elif [[ -n "$env_source" ]]; then
  install -m 600 "$env_source" "$tmp_dir/config.env"
else
  install -m 600 "$tmp_dir/server-monitor.env.example" "$tmp_dir/config.env"
  config_ready=false
  if [[ -t 0 && "$start_service" == true ]]; then
    read -r -s -p 'TELEGRAM_BOT_TOKEN: ' token
    printf '\n'
    read -r -p 'TELEGRAM_CHAT_ID: ' chat_id
    [[ "$token" =~ ^[0-9]+:[A-Za-z0-9_-]+$ ]] || die "Format token Telegram tidak valid"
    [[ "$chat_id" =~ ^-?[0-9]+$ || "$chat_id" =~ ^@[A-Za-z0-9_]+$ ]] || die "Format chat ID tidak valid"
    # Append without executing dotenv as shell code; input is restricted above.
    printf '\nTELEGRAM_BOT_TOKEN=%s\nTELEGRAM_CHAT_ID=%s\n' "$token" "$chat_id" >> "$tmp_dir/config.env"
    unset token chat_id
    config_ready=true
  fi
fi

if ! getent group server-monitor >/dev/null; then
  groupadd --system server-monitor
fi
if ! id server-monitor >/dev/null 2>&1; then
  nologin=$(command -v nologin || true)
  [[ -n "$nologin" ]] || nologin=/bin/false
  useradd --system --gid server-monitor --no-create-home --shell "$nologin" server-monitor
fi
install -d -o root -g root -m 755 /opt/server-monitor/bin
install -d -o root -g server-monitor -m 750 /etc/server-monitor
if [[ ! -e "$config" ]]; then
  install -o root -g server-monitor -m 640 "$tmp_dir/config.env" "$config"
fi
# Rename on the same filesystem avoids overwriting an executable still in use.
install -o root -g root -m 755 "$tmp_dir/$asset" "$binary.new"
mv -f -- "$binary.new" "$binary"
install -o root -g root -m 644 "$tmp_dir/server-monitor.service" "$unit"
systemctl daemon-reload

if [[ "$start_service" == true && "$config_ready" == true ]]; then
  systemctl enable server-monitor.service
  systemctl restart server-monitor.service || die "Gagal menjalankan service; cek journalctl -u server-monitor -n 50"
  systemctl is-active --quiet server-monitor.service || die "Service tidak aktif; cek journalctl -u server-monitor -n 50"
  printf 'Server Monitor %s terpasang dan service aktif.\n' "$version"
else
  printf 'Server Monitor %s terpasang; installer tidak mengubah status service.\n' "$version"
  printf 'Lengkapi %s, lalu jalankan:\n' "$config"
  printf '  sudo systemctl enable --now server-monitor\n'
  printf '  sudo systemctl restart server-monitor\n'
fi
printf 'Log: sudo journalctl -u server-monitor -f\n'
