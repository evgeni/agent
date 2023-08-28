package simple

import (
	"context"
	"os"
	"path"
	"path/filepath"
	"sync"
	"time"

	"github.com/grafana/agent/component/prometheus/remote/simple/pebble"

	"github.com/go-kit/log/level"
	"github.com/grafana/agent/pkg/flow/logging"
	"github.com/prometheus/client_golang/prometheus"
)

// dbstore is a helper interface around the bookmark and sample stores.
type dbstore struct {
	mut           sync.RWMutex
	directory     string
	l             *logging.Logger
	ttl           time.Duration
	sampleDB      SignalDB
	bookmark      SignalDB
	ctx           context.Context
	metrics       *dbmetrics
	oldestInUsKey uint64
}

const (
	MetricSignal int8 = iota
	HistogramSignal
	FloathistogramSignal
	MetadataSignal
	ExemplarSignal
	BookmarkType
)

func newDBStore(ttl time.Duration, directory string, r prometheus.Registerer, l *logging.Logger) (*dbstore, error) {
	bookmark, err := pebble.NewDB(path.Join(directory, "bookmark"), GetValue, GetType, l)
	if err != nil {
		return nil, err
	}
	sample, err := pebble.NewDB(path.Join(directory, "sample"), GetValue, GetType, l)
	if err != nil {
		return nil, err
	}
	store := &dbstore{
		bookmark:  bookmark,
		sampleDB:  sample,
		ttl:       ttl,
		l:         l,
		directory: directory,
	}

	dbm := newDbMetrics(r, store)
	store.metrics = dbm

	return store, nil
}

func (dbs *dbstore) Run(ctx context.Context) {
	dbs.ctx = ctx
	// Evict on startup to clean up any TTL files.
	dbs.evict()
	<-ctx.Done()
}

func (dbs *dbstore) WriteBookmark(key string, value any) error {
	return dbs.bookmark.WriteValue([]byte(key), value, 0*time.Second)
}

func (dbs *dbstore) GetBookmark(key string) (*Bookmark, bool) {
	bk, found, _ := dbs.bookmark.GetValueByString(key)
	if bk == nil {
		return &Bookmark{Key: 1}, false
	}
	return bk.(*Bookmark), found
}

func (dbs *dbstore) WriteSignal(value any) (uint64, error) {
	start := time.Now()
	defer dbs.metrics.writeTime.Observe(time.Since(start).Seconds())

	key, err := dbs.sampleDB.WriteValueWithAutokey(value, dbs.ttl)
	dbs.metrics.currentKey.Set(float64(key))
	level.Debug(dbs.l).Log("msg", "writing signals to WAL", "key", key)
	return key, err
}

func (dbs *dbstore) GetOldestKey() uint64 {
	return dbs.sampleDB.GetOldestKey()
}

func (dbs *dbstore) GetNextKey(k uint64) uint64 {
	return dbs.sampleDB.GetNextKey(k)
}

func (dbs *dbstore) UpdateOldestKey(k uint64) {
	dbs.mut.Lock()
	defer dbs.mut.Unlock()
	dbs.oldestInUsKey = k
}

func (dbs *dbstore) GetSignal(key uint64) (any, bool) {
	start := time.Now()
	defer dbs.metrics.readTime.Observe(time.Since(start).Seconds())

	val, found, err := dbs.sampleDB.GetValueByKey(key)
	if err != nil {
		level.Error(dbs.l).Log("error finding key", err, "key", key)
		return nil, false
	}
	return val, found
}

func (dbs *dbstore) getKeyCount() uint64 {
	keys, _ := dbs.sampleDB.GetKeys()
	return uint64(len(keys))
}

func (dbs *dbstore) getFileSize() float64 {
	return DirSize(dbs.directory)
}

func (dbs *dbstore) sampleCount() float64 {
	return float64(dbs.sampleDB.SeriesCount())
}

func (dbs *dbstore) averageCompressionRatio() float64 {
	return dbs.sampleDB.AverageCompressionRatio()
}

func (dbs *dbstore) evict() {
	dbs.mut.Lock()
	defer dbs.mut.Unlock()

	start := time.Now()
	defer dbs.metrics.evictionTime.Observe(time.Since(start).Seconds())
	err := dbs.bookmark.Evict()
	if err != nil {
		level.Error(dbs.l).Log("msg", "failure evicting bookmark db", "err", err)
	}
	err = dbs.sampleDB.Evict()
	if err != nil {
		level.Error(dbs.l).Log("msg", "failure evicting sample db", "err", err)
	}
}

func DirSize(path string) float64 {
	var size int64
	_ = filepath.Walk(path, func(_ string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if !info.IsDir() {
			size += info.Size()
		}
		return nil
	})
	return float64(size)
}