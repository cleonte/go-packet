package main

// Packet's one database. Numbers about the path only: no hostnames visited, no apps, no traffic.
// The schema matches the Rust original, so either can read the other's file.

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"

	_ "modernc.org/sqlite"
)

const (
	retainDays        = 90
	schemaVersion     = 2
	baselineMinRounds = 30 // good rounds needed before the baseline replaces fixed thresholds
	roundCols         = "at_ms,link,rssi,noise,tx_mbps,channel,band,gw_ms,gw_loss,gw_jitter,edge_ms,edge_loss,edge_jitter,edge_colo,dns_ms,r0,r1,r2,r3,r4,r5,r6,r7,expensive,level,layer"
)

// dataDir is the one place Packet decides where it writes. PACKET_DATA_DIR overrides it.
func dataDir() string {
	if d := os.Getenv("PACKET_DATA_DIR"); d != "" {
		return d
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, "Library/Application Support/Packet")
}

func openStore() (*sql.DB, error) {
	dir := dataDir()
	// Owner-only: the history is nobody else's business on a shared Mac.
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	_ = os.Chmod(dir, 0o700)
	return openStoreAt(filepath.Join(dir, "packet.sqlite"))
}

func openStoreAt(path string) (*sql.DB, error) {
	db, err := sql.Open("sqlite", "file:"+path+"?_pragma=busy_timeout(2000)&_pragma=journal_mode(WAL)")
	if err != nil {
		return nil, err
	}
	for _, suffix := range []string{"", "-wal", "-shm"} {
		_ = os.Chmod(path+suffix, 0o600)
	}
	var version int
	if err := db.QueryRow("PRAGMA user_version").Scan(&version); err != nil {
		return nil, err
	}
	if version < schemaVersion {
		for _, q := range []string{`CREATE TABLE IF NOT EXISTS rounds(
			at_ms INTEGER PRIMARY KEY, link INTEGER NOT NULL,
			rssi INTEGER, noise INTEGER, tx_mbps REAL, channel INTEGER, band INTEGER,
			gw_ms REAL, gw_loss REAL, gw_jitter REAL,
			edge_ms REAL, edge_loss REAL, edge_jitter REAL, edge_colo TEXT,
			dns_ms REAL,
			r0 REAL, r1 REAL, r2 REAL, r3 REAL, r4 REAL, r5 REAL, r6 REAL, r7 REAL,
			expensive INTEGER NOT NULL, level INTEGER NOT NULL, layer INTEGER NOT NULL
		) STRICT`, `CREATE TABLE IF NOT EXISTS bandwidth(
			at_ms INTEGER PRIMARY KEY, trigger INTEGER NOT NULL,
			down_mbps REAL, up_mbps REAL,
			down_bytes INTEGER NOT NULL, up_bytes INTEGER NOT NULL
		) STRICT`} {
			if _, err := db.Exec(q); err != nil {
				return nil, err
			}
		}
		// Version 2 added responsiveness under load (rpm) from Test Now.
		if _, err := db.Exec("ALTER TABLE bandwidth ADD COLUMN rpm REAL"); err != nil {
			return nil, err
		}
		if _, err := db.Exec(fmt.Sprintf("PRAGMA user_version = %d", schemaVersion)); err != nil {
			return nil, err
		}
	}
	return db, nil
}

// nullable converts an optional number for a query argument.
func nullable(p *float64) any {
	if p == nil {
		return nil
	}
	return *p
}

func insertRound(db *sql.DB, r *Round, v Verdict) error {
	if r.AtMs <= 0 {
		return fmt.Errorf("round has no timestamp")
	}
	if len(r.Colo) > 5 {
		return fmt.Errorf("colo is a data-center code")
	}
	var rssi, noise, ch, band, tx any
	if w := r.WiFi; w != nil {
		rssi, noise, tx, ch, band = w.Rssi, w.Noise, w.TxMbps, w.Channel, w.Band
	}
	var colo any
	if r.Colo != "" {
		colo = r.Colo
	}
	exp := 0
	if r.Expensive {
		exp = 1
	}
	_, err := db.Exec("INSERT OR REPLACE INTO rounds VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)",
		r.AtMs, int(r.Link), rssi, noise, tx, ch, band,
		nullable(r.Gateway.Ms), nullable(r.Gateway.Loss), nullable(r.Gateway.Jitter),
		nullable(r.Edge.Ms), nullable(r.Edge.Loss), nullable(r.Edge.Jitter), colo, nullable(r.DnsMs),
		nullable(r.Regions[0]), nullable(r.Regions[1]), nullable(r.Regions[2]), nullable(r.Regions[3]),
		nullable(r.Regions[4]), nullable(r.Regions[5]), nullable(r.Regions[6]), nullable(r.Regions[7]),
		exp, int(v.Level), int(v.At))
	return err
}

func insertBandwidth(db *sql.DB, b Bandwidth) error {
	if b.AtMs <= 0 || b.DownBytes < 0 || b.UpBytes < 0 {
		return fmt.Errorf("bad bandwidth row")
	}
	_, err := db.Exec("INSERT OR REPLACE INTO bandwidth VALUES (?,?,?,?,?,?,?)",
		b.AtMs, int(b.Trigger), nullable(b.DownMbps), nullable(b.UpMbps), b.DownBytes, b.UpBytes, nullable(b.Rpm))
	return err
}

func prune(db *sql.DB, now int64) {
	cutoff := now - retainDays*86_400_000
	_, _ = db.Exec("DELETE FROM rounds WHERE at_ms < ?", cutoff)
	_, _ = db.Exec("DELETE FROM bandwidth WHERE at_ms < ?", cutoff)
}

type Stored struct {
	Round   *Round
	Verdict Verdict
}

type scanner interface{ Scan(...any) error }

func scanStored(row scanner) (Stored, error) {
	var (
		atMs, link, expensive, level, layer            int64
		rssi, noise, channel, band                     sql.NullInt64
		tx, gwMs, gwLoss, gwJit, eMs, eLoss, eJit, dns sql.NullFloat64
		colo                                           sql.NullString
		reg                                            [8]sql.NullFloat64
	)
	err := row.Scan(&atMs, &link, &rssi, &noise, &tx, &channel, &band, &gwMs, &gwLoss, &gwJit,
		&eMs, &eLoss, &eJit, &colo, &dns,
		&reg[0], &reg[1], &reg[2], &reg[3], &reg[4], &reg[5], &reg[6], &reg[7], &expensive, &level, &layer)
	if err != nil {
		return Stored{}, err
	}
	nf := func(n sql.NullFloat64) *float64 {
		if !n.Valid {
			return nil
		}
		return &n.Float64
	}
	r := &Round{AtMs: atMs, Link: LinkKind(link), Expensive: expensive != 0,
		Gateway: Layer{nf(gwMs), nf(gwLoss), nf(gwJit)}, Edge: Layer{nf(eMs), nf(eLoss), nf(eJit)},
		Colo: colo.String, DnsMs: nf(dns)}
	if rssi.Valid {
		r.WiFi = &WiFi{Rssi: rssi.Int64, Noise: noise.Int64, TxMbps: tx.Float64, Channel: channel.Int64, Band: band.Int64}
	}
	for i := range reg {
		r.Regions[i] = nf(reg[i])
	}
	// A router that was pinged but never answered is stored with loss and no latency.
	r.GatewayKnown = gwMs.Valid || gwLoss.Valid
	return Stored{r, storedVerdict(level, layer)}, nil
}

func queryRounds(db *sql.DB, q string, args ...any) ([]Stored, error) {
	rows, err := db.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Stored
	for rows.Next() {
		s, err := scanStored(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

// nearestRound is the stored round closest to atMs, within windowMs.
func nearestRound(db *sql.DB, atMs, windowMs int64) (*Stored, error) {
	rs, err := queryRounds(db, "SELECT "+roundCols+" FROM rounds WHERE at_ms BETWEEN ? AND ? ORDER BY ABS(at_ms - ?) LIMIT 1",
		atMs-windowMs, atMs+windowMs, atMs)
	if err != nil || len(rs) == 0 {
		return nil, err
	}
	return &rs[0], nil
}

func roundsBetween(db *sql.DB, from, to int64) ([]Stored, error) {
	return queryRounds(db, "SELECT "+roundCols+" FROM rounds WHERE at_ms BETWEEN ? AND ? ORDER BY at_ms", from, to)
}

func scanBW(row scanner) (Bandwidth, error) {
	var b Bandwidth
	var trig int
	var down, up, rpm sql.NullFloat64
	if err := row.Scan(&b.AtMs, &trig, &down, &up, &b.DownBytes, &b.UpBytes, &rpm); err != nil {
		return b, err
	}
	b.Trigger = Trigger(trig)
	for _, p := range []struct {
		n sql.NullFloat64
		d **float64
	}{{down, &b.DownMbps}, {up, &b.UpMbps}, {rpm, &b.Rpm}} {
		if p.n.Valid {
			v := p.n.Float64
			*p.d = &v
		}
	}
	return b, nil
}

func queryBW(db *sql.DB, q string, args ...any) ([]Bandwidth, error) {
	rows, err := db.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Bandwidth
	for rows.Next() {
		b, err := scanBW(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, b)
	}
	return out, rows.Err()
}

func firstBW(db *sql.DB, q string, args ...any) *Bandwidth {
	bs, err := queryBW(db, q, args...)
	if err != nil || len(bs) == 0 {
		return nil
	}
	return &bs[0]
}

// latestFull is the last hourly or Test Now run: the line's real speed.
func latestFull(db *sql.DB) *Bandwidth {
	return firstBW(db, "SELECT * FROM bandwidth WHERE trigger IN (2,3) ORDER BY at_ms DESC LIMIT 1")
}
func latestQuick(db *sql.DB) *Bandwidth {
	return firstBW(db, "SELECT * FROM bandwidth WHERE trigger IN (0,1) ORDER BY at_ms DESC LIMIT 1")
}
func nearestBandwidth(db *sql.DB, atMs, windowMs int64) *Bandwidth {
	return firstBW(db, "SELECT * FROM bandwidth WHERE at_ms BETWEEN ? AND ? ORDER BY ABS(at_ms - ?) LIMIT 1",
		atMs-windowMs, atMs+windowMs, atMs)
}
func bandwidthBetween(db *sql.DB, from, to int64) ([]Bandwidth, error) {
	return queryBW(db, "SELECT * FROM bandwidth WHERE at_ms BETWEEN ? AND ? ORDER BY at_ms", from, to)
}

func medianOf(db *sql.DB, col string, since int64) *float64 {
	rows, err := db.Query("SELECT "+col+" FROM rounds WHERE at_ms >= ? AND level = 0 AND "+col+" IS NOT NULL", since)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var v []float64
	for rows.Next() {
		var x float64
		if rows.Scan(&x) == nil {
			v = append(v, x)
		}
	}
	return median(v)
}

// loadBaseline: medians of the last 7 days of good rounds, and of quick-check downloads (only
// quick checks are compared with quick checks).
func loadBaseline(db *sql.DB, now int64) Baseline {
	since := now - 7*86_400_000
	var good int
	if db.QueryRow("SELECT COUNT(*) FROM rounds WHERE at_ms >= ? AND level = 0", since).Scan(&good) != nil || good < baselineMinRounds {
		return Baseline{}
	}
	var b Baseline
	for i := range b.Regions {
		b.Regions[i] = medianOf(db, fmt.Sprintf("r%d", i), since)
	}
	b.EdgeMs = medianOf(db, "edge_ms", since)
	b.DnsMs = medianOf(db, "dns_ms", since)
	if rows, err := db.Query("SELECT down_mbps FROM bandwidth WHERE at_ms >= ? AND trigger IN (0,1) AND down_mbps IS NOT NULL", since); err == nil {
		defer rows.Close()
		var downs []float64
		for rows.Next() {
			var x float64
			if rows.Scan(&x) == nil {
				downs = append(downs, x)
			}
		}
		if len(downs) >= 6 {
			b.DownMbps = median(downs)
		}
	}
	return b
}

// recentScheduledDown is a quick-check download from the last 15 minutes, for the verdict.
func recentScheduledDown(db *sql.DB, now int64) *float64 {
	var d sql.NullFloat64
	if db.QueryRow("SELECT down_mbps FROM bandwidth WHERE at_ms >= ? AND trigger IN (0,1) ORDER BY at_ms DESC LIMIT 1", now-15*60_000).Scan(&d) != nil || !d.Valid {
		return nil
	}
	return &d.Float64
}
