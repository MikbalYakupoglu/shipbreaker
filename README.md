# Shipbreaker

Docker host'undaki zombie servisleri tespit eden hafif bir izleme aracı. Çalışan ama CPU, ağ ve disk I/O tüketimi ihmal edilebilir düzeyde olan konteynerleri belirler.

## Nasıl Çalışır

Shipbreaker üç arka plan görevi çalıştırır:

- **Watcher** — her konteynerin CPU, ağ ve disk I/O metriklerini `SHIPBREAKER_SAMPLE_INTERVAL_SEC` (varsayılan: 5 s) aralıklarla örnekler ve SQLite'a yazar.
- **Aggregator** — ham satırları saatlik bucketlara toplar, veritabanını küçük tutar.
- **Retention** — eski satırları düzenli aralıklarla temizler.

Bir servis **ZOMBIE** olarak işaretlenmek için yapılandırılmış gözlem penceresi boyunca CPU, ağ ve disk eşiklerinin **tamamının** altında kalmalıdır.

```
ZOMBIE   — tüm eşiklerin altında (gözlem penceresi boyunca)
ACTIVE   — en az bir eşiğin üzerinde
UNKNOWN  — yeterli veri yok (< MIN_SAMPLES)
```

## Kurulum

### Docker Compose (önerilen)

```yaml
services:
  shipbreaker:
    image: ghcr.io/mikbal/shipbreaker:latest
    container_name: shipbreaker
    restart: unless-stopped
    ports:
      - "7777:7777"
    volumes:
      - /var/run/docker.sock:/var/run/docker.sock:ro
      - shipbreaker_data:/data
    environment:
      SHIPBREAKER_USER: admin
      SHIPBREAKER_PASSWORD: s3cr3t

volumes:
  shipbreaker_data:
```

```bash
docker compose up -d
```

Arayüz `http://localhost:7777` adresinde açılır.

### Docker

```bash
docker run -d \
  -p 7777:7777 \
  -v /var/run/docker.sock:/var/run/docker.sock:ro \
  -v shipbreaker_data:/data \
  -e SHIPBREAKER_USER=admin \
  -e SHIPBREAKER_PASSWORD=s3cr3t \
  --name shipbreaker \
  ghcr.io/mikbal/shipbreaker:latest
```

### Dokploy

Dokploy'da **Application** tipi olarak deploy edilirken aşağıdaki ayarların yapılandırılması gerekir.

**Environment**

```
SHIPBREAKER_USER=admin
SHIPBREAKER_PASSWORD=s3cr3t
SHIPBREAKER_SESSION_SECRET=rastgele_uzun_bir_string
SHIPBREAKER_DB_PATH=/data/shipbreaker.db
```

**Volumes / Mounts**

Docker socket için **Bind Mount**:

| Alan | Değer |
|---|---|
| Host Path | `/var/run/docker.sock` |
| Mount Path | `/var/run/docker.sock` |

Veritabanı kalıcılığı için **Volume Mount**:

| Alan | Değer |
|---|---|
| Volume Name | `shipbreaker_data` |
| Mount Path | `/data` |

> Docker socket mount edilmezse uygulama başlar fakat konteynerlere ulaşamaz.

### Kaynaktan Derleme

Go 1.25+ ve Node 22+ gereklidir.

```bash
git clone https://github.com/mikbal/shipbreaker.git
cd shipbreaker

# UI'yi derle
cd ui && npm ci && npm run build && cd ..

# Go binary'sini derle
CGO_ENABLED=0 go build -o breaker ./cmd/breaker

# Çalıştır
./breaker serve --bind 127.0.0.1
```

## Komutlar

### `breaker serve`

Daemon'u başlatır: Watcher + Aggregator + Retention + HTTP API + Web UI.

```bash
breaker serve [flags]

Flags:
  --bind string    bind adresi (varsayılan: 0.0.0.0)
  --port int       dinleme portu (varsayılan: 7777)
  --db string      SQLite veritabanı yolu (varsayılan: /data/shipbreaker.db)
  --tz string      gösterim zaman dilimi (varsayılan: UTC)
  --config string  YAML config dosyası yolu
```

> **Not:** Loopback dışı bir adrese bağlanırken `SHIPBREAKER_USER` ve `SHIPBREAKER_PASSWORD` zorunludur.

### `breaker scan`

Mevcut veritabanı üzerinde tek seferlik zombie taraması yapar, sonuçları stdout'a basar. Docker daemon bağlantısı kurmaz, yeni örnek yazmaz.

```bash
breaker scan [--db /path/to/shipbreaker.db] [--config config.yaml]
```

## Yapılandırma

Öncelik sırası: **CLI flag > ortam değişkeni > YAML config > varsayılan**

### Ortam Değişkenleri

| Değişken | Açıklama | Varsayılan |
|---|---|---|
| `SHIPBREAKER_USER` | HTTP basic-auth kullanıcı adı | — |
| `SHIPBREAKER_PASSWORD` | HTTP basic-auth şifresi | — |
| `SHIPBREAKER_SESSION_SECRET` | Oturum çerezi imzalama anahtarı | *(DB'de otomatik oluşturulur)* |
| `SHIPBREAKER_BIND` | Bind adresi | `0.0.0.0` |
| `SHIPBREAKER_PORT` | Dinleme portu | `7777` |
| `SHIPBREAKER_DB_PATH` | SQLite veritabanı yolu | `/data/shipbreaker.db` |
| `SHIPBREAKER_TZ` | Gösterim zaman dilimi | `UTC` |
| `SHIPBREAKER_SAMPLE_INTERVAL_SEC` | Docker stats örnekleme aralığı (saniye) | `60` |
| `SHIPBREAKER_LIVE_INTERVAL_SEC` | "Canlı takip" modunda örnekleme aralığı (saniye) | `5` |
| `SHIPBREAKER_CPU_THRESHOLD_PCT` | Zombie CPU eşiği (çekirdek başı %) | `5.0` |
| `SHIPBREAKER_NET_THRESHOLD_PER_DAY` | Zombie ağ eşiği (byte/gün) | `1572864` (1.5 MB) |
| `SHIPBREAKER_DISK_THRESHOLD_PER_DAY` | Zombie disk I/O eşiği (byte/gün) | `7340032` (7 MB) |

### YAML Config

```yaml
bind: 0.0.0.0
port: 7777
db_path: /data/shipbreaker.db

sample_interval_sec: 60   # Docker stats örnekleme aralığı (saniye)
live_interval_sec: 5      # "Canlı takip" modunda örnekleme aralığı (saniye)
window_days: 7
min_samples: 84          # 7 günün %50'si

cpu_threshold_pct: 5.0
net_threshold_per_day: 1572864   # 1.5 MB
disk_threshold_per_day: 7340032  # 7 MB

raw_retention_days: 3
hourly_retention_days: 35

tz: Europe/Istanbul
```

```bash
breaker serve --config /etc/shipbreaker/config.yaml
```

## Güvenlik

- `SHIPBREAKER_USER` ve `SHIPBREAKER_PASSWORD` set edilmeden `0.0.0.0`'a bağlanmak engellenir (fail-closed).
- `127.0.0.1`'e bağlanılırken kimlik doğrulama isteğe bağlıdır; ancak konsola uyarı yazılır.
- Oturum anahtarı belirtilmezse her yeniden başlatmada geçerliliğini korumak için DB'de saklanır.

## Sağlık Kontrolü

```
GET /healthz
```

200 döndürüyorsa servis ayaktadır.

## Lisans

Apache 2.0 — bkz. [LICENSE](LICENSE)
