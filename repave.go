package main

// `paver repave` brings rasters paver has already published up to what it
// publishes now. A raster made from a raster source is rebuilt from that
// source with the parameters admin sends crop-raster; any other raster is
// re-encoded as it is, because the parameters chosen when it was paved are not
// stored. An original is only ever read: each result is written under a new
// key, so cached copies stay valid and a rollback is a database revert.
//
// Every job has a key made of its inputs (parameters, the ETags of every file
// it reads, the encoding version); a job whose key has a settled manifest entry
// is skipped, so reruns redo only what changed. The SQL that repoints datasets
// (and back) is built from the manifest.

import (
	"bufio"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"math"
	"net/http"
	"os"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/energyaccessexplorer/gdal"
)

// One line of the job list: a JSON object (see repave.sql), or a bare
// endpoint, which is re-encoded.
type repave_job struct {
	Endpoint  string `json:"endpoint"`
	DatasetID string `json:"dataset,omitempty"`
	Geography string `json:"geography,omitempty"`
	Routine   string `json:"routine,omitempty"`

	routine_params
}

const repave_reencode = "re-encode"

func (j repave_job) routine() string {
	if j.Routine == "" {
		return repave_reencode
	}
	return j.Routine
}

type repave_entry struct {
	Old       string `json:"old"`
	New       string `json:"new,omitempty"`
	Dataset   string `json:"dataset,omitempty"`
	Routine   string `json:"routine,omitempty"`
	Key       string `json:"key,omitempty"`
	Bucket    string `json:"bucket,omitempty"`
	Status    string `json:"status"`
	OldBytes  int64  `json:"old_bytes,omitempty"`
	NewBytes  int64  `json:"new_bytes,omitempty"`
	OldBands  int    `json:"old_bands,omitempty"`
	NewBands  int    `json:"new_bands,omitempty"`
	Predictor string `json:"predictor,omitempty"`
	Error     string `json:"error,omitempty"`
}

const (
	repave_done      = "done"      // verified, written under New
	repave_verified  = "verified"  // dry run: verified, not written
	repave_unchanged = "unchanged" // nothing to gain: keep the original
	repave_mismatch  = "mismatch"  // a rebuild that differs where it must not; not written
	repave_skipped   = "skipped"   // not a GeoTIFF, or outside the target bucket
	repave_failed    = "failed"
)

func repave_main(args []string) error {
	fs := flag.NewFlagSet("repave", flag.ExitOnError)

	list := fs.String("list", "", "Job list: JSON lines from repave.sql, or one raster endpoint per line")
	manifest := fs.String("manifest", "", "JSON-lines manifest; appended to, and read to resume")
	bucket := fs.String("bucket", "", "Bucket to write to (from -buckets); none is a dry run")
	apply := fs.Bool("apply", false, "Allow writing to a bucket that is not local")
	jobs := fs.Int("j", 2, "Jobs run at once")

	fs.StringVar(&buckets, "buckets", "/etc/paver-buckets.json", "")
	fs.StringVar(&tmpdir, "tmpdir", os.TempDir(), "")

	fs.Parse(args)

	logger = log.New(os.Stderr, "", log.LstdFlags)

	if *list == "" || *manifest == "" {
		return errors.New("repave needs -list and -manifest")
	}

	var target *s3config
	if *bucket != "" {
		conf, err := s3config_get(*bucket)
		if err != nil {
			return err
		}
		if conf.Provider != local_provider && !*apply {
			return fmt.Errorf("bucket '%s' is not local: writing to it needs -apply", *bucket)
		}
		target = &conf
	}

	todo, err := read_jobs(*list)
	if err != nil {
		return err
	}

	previous, err := manifest_read(*manifest)
	if err != nil {
		return err
	}

	out, err := os.OpenFile(*manifest, os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0644)
	if err != nil {
		return err
	}
	defer out.Close()

	var mu sync.Mutex
	record := func(e repave_entry) {
		mu.Lock()
		defer mu.Unlock()

		j, _ := json.Marshal(e)
		out.Write(append(j, '\n'))
		previous[e.Old] = e

		logger.Printf("%-9s %-11s %s %d -> %d %s", e.Status, e.Routine, e.Old, e.OldBytes, e.NewBytes, e.Error)
	}

	queue := make(chan repave_job)
	var wg sync.WaitGroup

	for i := 0; i < max(1, *jobs); i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for job := range queue {
				key, err := repave_key(job)
				if err != nil {
					record(repave_entry{Old: job.Endpoint, Dataset: job.DatasetID, Routine: job.routine(), Status: repave_failed, Error: err.Error()})
					continue
				}

				mu.Lock()
				e, ok := previous[job.Endpoint]
				mu.Unlock()

				if ok && repave_settled(e, *bucket, key) {
					continue
				}

				record(repave_one(job, key, target))
			}
		}()
	}

	for _, job := range todo {
		queue <- job
	}
	close(queue)
	wg.Wait()

	if err := repave_sql(previous, *manifest); err != nil {
		return err
	}

	repave_summary(previous)

	return nil
}

// repave_settled says a previous run's entry needs no more work for bucket
// ("" for a dry run), given the job's key now.
func repave_settled(e repave_entry, bucket string, key string) bool {
	if e.Key != key {
		return false
	}

	switch e.Status {
	case repave_unchanged, repave_mismatch:
		return true
	case repave_skipped, repave_done:
		return e.Bucket == bucket
	case repave_verified:
		return bucket == ""
	default:
		return false
	}
}

// repave_key identifies a job's inputs: what it reads, and how it encodes.
func repave_key(j repave_job) (string, error) {
	parts := []string{j.routine(), deliverable_version}

	reads := []string{j.Endpoint}
	if j.routine() == "crop-raster" {
		p := j.routine_params
		p.S3 = s3config{}
		parts = append(parts, idempotency_key("crop-raster", p))
		reads = append(reads, j.routine_params.Dataset, j.Reference, j.Base)
	}

	for _, url := range reads {
		v, err := object_version(url)
		if err != nil {
			return "", err
		}
		parts = append(parts, v)
	}

	h := sha256.Sum256([]byte(strings.Join(parts, "\n")))

	return hex.EncodeToString(h[:]), nil
}

var http_client = &http.Client{Timeout: 10 * time.Minute}

// object_version is what changes when the object at url does.
func object_version(url string) (string, error) {
	r, err := http_client.Head(url)
	if err != nil {
		return "", err
	}
	r.Body.Close()

	if r.StatusCode != http.StatusOK {
		return "", fmt.Errorf("HEAD %s: %s", url, r.Status)
	}

	if etag := r.Header.Get("ETag"); etag != "" {
		return url + " " + etag, nil
	}

	return url + " " + r.Header.Get("Last-Modified") + " " + r.Header.Get("Content-Length"), nil
}

func repave_one(j repave_job, key string, target *s3config) repave_entry {
	e := repave_entry{Old: j.Endpoint, Dataset: j.DatasetID, Routine: j.routine(), Key: key}
	if target != nil {
		e.Bucket = target.Name
	}

	if target != nil && target.Provider != local_provider && !strings.HasPrefix(j.Endpoint, s3endpoint(*target, "")) {
		e.Status, e.Error = repave_skipped, "outside the target bucket"
		return e
	}

	in, err := download(j.Endpoint)
	if err != nil {
		e.Status, e.Error = repave_failed, err.Error()
		return e
	}
	defer trash(in)

	e.OldBytes = file_size(in)

	if !is_geotiff(in) {
		e.Status, e.Error = repave_skipped, "not a GeoTIFF"
		return e
	}

	e.OldBands = band_count(in)

	var out filename

	switch j.routine() {
	case repave_reencode:
		out, e.Predictor, err = raster_deliverable(in)
		if err != nil {
			e.Status, e.Error = repave_failed, err.Error()
			return e
		}

	case "crop-raster":
		out, e.Predictor, err = repave_crop(j, in)
		if errors.Is(err, err_mismatch) {
			e.Status, e.Error = repave_mismatch, err.Error()
			return e
		}
		if err != nil {
			e.Status, e.Error = repave_failed, err.Error()
			return e
		}

	default:
		e.Status, e.Error = repave_failed, "unknown routine "+j.Routine
		return e
	}

	e.NewBytes, e.NewBands = file_size(out), band_count(out)

	unchanged := j.routine() == repave_reencode && e.NewBytes >= e.OldBytes
	if !unchanged && j.routine() != repave_reencode {
		unchanged = rasters_identical(in, out) == nil
	}

	if unchanged {
		trash(out)
		e.Status = repave_unchanged
		return e
	}

	if target == nil {
		trash(out)
		e.Status = repave_verified
		return e
	}

	// s3put names the object after the file's uuid: a fresh one.
	named := _filename()
	if err := os.Rename(out, named); err != nil {
		trash(out)
		e.Status, e.Error = repave_failed, err.Error()
		return e
	}
	defer trash(named)

	if !s3put(named, *target) {
		e.Status, e.Error = repave_failed, "upload failed"
		return e
	}

	e.Status, e.New = repave_done, s3endpoint(*target, _uuid(named))

	return e
}

var err_mismatch = errors.New("rebuild differs from the published raster")

// repave_crop rebuilds a raster from its source as crop-raster does now, and
// checks it against the published one: same grid, and the same display band.
// The aggregation bands are new data, so only their presence is checked.
func repave_crop(j repave_job, published filename) (filename, string, error) {
	cropped, err := crop_raster_output(noop_reporter, j.routine_params)
	if err != nil {
		return "", "", err
	}

	out, predictor, err := raster_deliverable(cropped)
	trash(cropped)
	if err != nil {
		return "", "", err
	}

	if err := display_band_matches(published, out); err != nil {
		trash(out)
		return "", "", fmt.Errorf("%w: %v", err_mismatch, err)
	}

	return out, predictor, nil
}

func noop_reporter(string, ...any) string { return "" }

func band_count(f filename) int {
	ds, err := gdal.Open(f, gdal.ReadOnly)
	if err != nil {
		return 0
	}
	defer ds.Close()

	return ds.RasterCount()
}

// display_band_matches checks rebuilt against published: same grid, three
// Float32 bands, and band 1 holding the same value in every cell, where a
// cell counts as empty by nodata, or by an alpha band in the published one.
func display_band_matches(published, rebuilt filename) error {
	dp, err := gdal.Open(published, gdal.ReadOnly)
	if err != nil {
		return err
	}
	defer dp.Close()

	dr, err := gdal.Open(rebuilt, gdal.ReadOnly)
	if err != nil {
		return err
	}
	defer dr.Close()

	width, height := dp.RasterXSize(), dp.RasterYSize()

	if width != dr.RasterXSize() || height != dr.RasterYSize() {
		return fmt.Errorf("size %dx%d vs %dx%d", width, height, dr.RasterXSize(), dr.RasterYSize())
	}

	if !geotransforms_equal(dp.GeoTransform(), dr.GeoTransform()) {
		return fmt.Errorf("geotransform %v vs %v", dp.GeoTransform(), dr.GeoTransform())
	}

	if !projections_equal(dp.Projection(), dr.Projection()) {
		return errors.New("projection differs")
	}

	if dr.RasterCount() != 3 {
		return fmt.Errorf("%d bands, want 3", dr.RasterCount())
	}

	for i := 1; i <= 3; i++ {
		if dt := dr.RasterBand(i).RasterDataType(); dt != gdal.Float32 {
			return fmt.Errorf("band %d is %v, want Float32", i, dt)
		}
	}

	pb := dp.RasterBand(1)
	pnodata, phas := pb.NoDataValue()

	var alpha *gdal.RasterBand
	for i := 2; i <= dp.RasterCount(); i++ {
		if b := dp.RasterBand(i); b.ColorInterp() == gdal.CI_AlphaBand {
			alpha = &b
		}
	}

	rb := dr.RasterBand(1)
	rnodata, rhas := rb.NoDataValue()

	empty := func(v, nodata float64, has bool) bool {
		return math.IsNaN(v) || (has && v == nodata)
	}

	const rows = 256
	var differ, holes, cells int

	for y := 0; y < height; y += rows {
		n := min(rows, height-y)

		p := make([]float64, width*n)
		r := make([]float64, width*n)

		if err := pb.IO(gdal.Read, 0, y, width, n, p, width, n, 0, 0); err != nil {
			return err
		}
		if err := rb.IO(gdal.Read, 0, y, width, n, r, width, n, 0, 0); err != nil {
			return err
		}

		var a []float64
		if alpha != nil {
			a = make([]float64, width*n)
			if err := alpha.IO(gdal.Read, 0, y, width, n, a, width, n, 0, 0); err != nil {
				return err
			}
		}

		for k := range p {
			pe := empty(p[k], pnodata, phas) || (a != nil && a[k] == 0)
			re := empty(r[k], rnodata, rhas)

			switch {
			case pe != re:
				holes++
			case !pe && float32(p[k]) != float32(r[k]):
				differ++
			}
		}

		cells += width * n
	}

	if differ > 0 || holes > 0 {
		return fmt.Errorf("band 1: %d of %d values differ, %d cells empty in one only", differ, cells, holes)
	}

	return nil
}

// Rasters written by different GDAL versions can differ in the last bits of
// a coordinate.
func geotransforms_equal(a, b [6]float64) bool {
	for i := range a {
		if math.Abs(a[i]-b[i]) > 1e-9*math.Max(1, math.Abs(a[i])) {
			return false
		}
	}
	return true
}

// ... and in how they spell the same projection.
func projections_equal(a, b string) bool {
	if a == b {
		return true
	}

	sa, sb := gdal.CreateSpatialReference(""), gdal.CreateSpatialReference("")
	defer sa.Destroy()
	defer sb.Destroy()

	if sa.FromWKT(a) != nil || sb.FromWKT(b) != nil {
		return false
	}

	return sa.IsSame(sb)
}

func download(url string) (filename, error) {
	r, err := http_client.Get(url)
	if err != nil {
		return "", err
	}
	defer r.Body.Close()

	if r.StatusCode != http.StatusOK {
		return "", fmt.Errorf("GET %s: %s", url, r.Status)
	}

	f := _filename()
	file, err := os.Create(f)
	if err != nil {
		return "", err
	}

	if _, err := io.Copy(file, r.Body); err != nil {
		file.Close()
		trash(f)
		return "", err
	}

	return f, file.Close()
}

// read_jobs reads a job list, one job per endpoint (the first one wins).
func read_jobs(f filename) ([]repave_job, error) {
	file, err := os.Open(f)
	if err != nil {
		return nil, err
	}
	defer file.Close()

	var jobs []repave_job
	seen := map[string]bool{}

	sc := bufio.NewScanner(file)
	sc.Buffer(make([]byte, 1<<20), 1<<20)

	for n := 1; sc.Scan(); n++ {
		l := strings.TrimSpace(sc.Text())
		if l == "" {
			continue
		}

		j := repave_job{Endpoint: l}
		if strings.HasPrefix(l, "{") {
			j = repave_job{}
			if err := json.Unmarshal([]byte(l), &j); err != nil {
				return nil, fmt.Errorf("%s:%d: %w", f, n, err)
			}
		}

		if j.Endpoint == "" {
			return nil, fmt.Errorf("%s:%d: no endpoint", f, n)
		}

		if j.routine() == "crop-raster" && (j.routine_params.Dataset == "" || j.Reference == "" || j.Base == "" || j.Config.Resample == "") {
			return nil, fmt.Errorf("%s:%d: crop-raster job without dataseturl, referenceurl, baseurl or config", f, n)
		}

		if !seen[j.Endpoint] {
			seen[j.Endpoint] = true
			jobs = append(jobs, j)
		}
	}

	return jobs, sc.Err()
}

// manifest_read returns the last entry recorded for every endpoint.
func manifest_read(f filename) (map[string]repave_entry, error) {
	entries := map[string]repave_entry{}

	file, err := os.Open(f)
	if errors.Is(err, os.ErrNotExist) {
		return entries, nil
	}
	if err != nil {
		return nil, err
	}
	defer file.Close()

	sc := bufio.NewScanner(file)
	sc.Buffer(make([]byte, 1<<20), 1<<20)

	for sc.Scan() {
		var e repave_entry
		if err := json.Unmarshal(sc.Bytes(), &e); err != nil {
			return nil, fmt.Errorf("%s: %w", f, err)
		}
		entries[e.Old] = e
	}

	return entries, sc.Err()
}

func sql_literal(s string) string {
	return "'" + strings.ReplaceAll(s, "'", "''") + "'"
}

// repoint_sql swaps one endpoint for another in every dataset that has it.
func repoint_sql(from, to string) string {
	return fmt.Sprintf(`update datasets set processed_files = (
	select jsonb_agg(case when f->>'endpoint' = %[1]s then jsonb_set(f, '{endpoint}', to_jsonb(%[2]s::text)) else f end order by o)
	from jsonb_array_elements(processed_files) with ordinality as e(f, o))
where processed_files @> jsonb_build_array(jsonb_build_object('endpoint', %[1]s::text));
`, sql_literal(from), sql_literal(to))
}

func repave_sql(entries map[string]repave_entry, manifest filename) error {
	var olds []string
	for old, e := range entries {
		if e.Status == repave_done {
			olds = append(olds, old)
		}
	}
	sort.Strings(olds)

	var apply, rollback strings.Builder
	apply.WriteString("begin;\n")
	rollback.WriteString("begin;\n")

	for _, old := range olds {
		apply.WriteString(repoint_sql(old, entries[old].New))
		rollback.WriteString(repoint_sql(entries[old].New, old))
	}

	apply.WriteString("commit;\n")
	rollback.WriteString("commit;\n")

	if err := os.WriteFile(manifest+".apply.sql", []byte(apply.String()), 0644); err != nil {
		return err
	}

	return os.WriteFile(manifest+".rollback.sql", []byte(rollback.String()), 0644)
}

func repave_summary(entries map[string]repave_entry) {
	type tally struct {
		count         map[string]int
		before, after int64
	}

	by := map[string]*tally{}
	for _, e := range entries {
		t := by[e.Routine]
		if t == nil {
			t = &tally{count: map[string]int{}}
			by[e.Routine] = t
		}

		t.count[e.Status]++
		if e.Status == repave_done || e.Status == repave_verified {
			t.before += e.OldBytes
			t.after += e.NewBytes
		}
	}

	for _, r := range []string{repave_reencode, "crop-raster"} {
		t := by[r]
		if t == nil {
			continue
		}

		c := t.count
		fmt.Printf("%s: done %d, verified %d, unchanged %d, mismatch %d, skipped %d, failed %d",
			r, c[repave_done], c[repave_verified], c[repave_unchanged], c[repave_mismatch], c[repave_skipped], c[repave_failed])

		if t.before > 0 {
			fmt.Printf("; %d -> %d bytes (%.1f%%)", t.before, t.after, 100*float64(t.after)/float64(t.before))
		}

		fmt.Println()
	}
}
