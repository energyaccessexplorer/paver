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
	Warning   string `json:"warning,omitempty"`
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
		var agree float64
		out, e.Predictor, agree, err = repave_crop(j, in)
		if err == nil && agree < 0.98 {
			e.Warning = fmt.Sprintf("%.0f%% of sampled cells differ from the source; it may have changed since the file was paved", 100*(1-agree))
		}
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

// disagreement is a check's verdict on a rebuild, as opposed to a failure to
// run the check: only a verdict settles a job, a failure is retried.
type disagreement struct{ error }

func disagrees(format string, a ...any) error {
	return disagreement{fmt.Errorf(format, a...)}
}

// repave_crop rebuilds a raster from its source on the outline raster's
// grid, and checks the result against what can be known independently: the
// grid the tool assumes, the cells the geography covers, and -- when the source
// is already in the grid's projection -- the source values themselves.
func repave_crop(j repave_job, published filename) (filename, string, float64, error) {
	cropped, err := crop_raster_output(noop_reporter, j.routine_params)
	if err != nil {
		return "", "", 1, err
	}

	out, predictor, err := raster_deliverable(cropped)
	trash(cropped)
	if err != nil {
		return "", "", 1, err
	}

	agree, err := crop_values_from_source(j.Dataset, j.Base, out, j.Config.Resample)

	if err := crop_verdict(crop_on_base_grid(j.Base, out), err, crop_retains_coverage(published, j.Base, out)); err != nil {
		trash(out)
		return "", "", 0, err
	}

	return out, predictor, agree, nil
}

// crop_verdict is the first check that could not run, or err_mismatch with
// every disagreement, or nil.
func crop_verdict(checks ...error) error {
	verdicts := []string{}

	for _, e := range checks {
		var d disagreement
		switch {
		case e == nil:
		case errors.As(e, &d):
			verdicts = append(verdicts, e.Error())
		default:
			return e
		}
	}

	if len(verdicts) > 0 {
		return fmt.Errorf("%w: %s", err_mismatch, strings.Join(verdicts, "; "))
	}

	return nil
}

// crop_on_base_grid: same size, geotransform and projection as the outline
// raster, and no value in a cell the outline raster leaves empty.
func crop_on_base_grid(base, out filename) error {
	b, err := gdal.Open(base, gdal.ReadOnly)
	if err != nil {
		return err
	}
	defer b.Close()

	o, err := gdal.Open(out, gdal.ReadOnly)
	if err != nil {
		return err
	}
	defer o.Close()

	if b.RasterXSize() != o.RasterXSize() || b.RasterYSize() != o.RasterYSize() {
		return disagrees("grid %dx%d, outline raster is %dx%d", o.RasterXSize(), o.RasterYSize(), b.RasterXSize(), b.RasterYSize())
	}
	if !geotransforms_equal(b.GeoTransform(), o.GeoTransform()) {
		return disagrees("geotransform %v, outline raster is %v", o.GeoTransform(), b.GeoTransform())
	}
	if !projections_equal(b.Projection(), o.Projection()) {
		return disagrees("projection differs from the outline raster")
	}
	if o.RasterCount() != 3 {
		return disagrees("%d bands, want 3", o.RasterCount())
	}

	outside, err := cells_outside_base(b, o)
	if err != nil {
		return err
	}
	if outside > 0 {
		return disagrees("%d cells hold a value outside the outline", outside)
	}

	return nil
}

func cells_outside_base(base, out gdal.Dataset) (int, error) {
	bb := base.RasterBand(1)
	base_nodata, has := bb.NoDataValue()
	ob := out.RasterBand(1)
	out_nodata, out_has := ob.NoDataValue()

	width, height := base.RasterXSize(), base.RasterYSize()

	const rows = 256
	outside := 0

	for y := 0; y < height; y += rows {
		n := min(rows, height-y)

		inside := make([]float64, width*n)
		values := make([]float64, width*n)

		if err := bb.IO(gdal.Read, 0, y, width, n, inside, width, n, 0, 0); err != nil {
			return 0, err
		}
		if err := ob.IO(gdal.Read, 0, y, width, n, values, width, n, 0, 0); err != nil {
			return 0, err
		}

		for k := range values {
			empty := math.IsNaN(inside[k]) || (has && inside[k] == base_nodata)
			valued := !math.IsNaN(values[k]) && !(out_has && values[k] == out_nodata)
			if empty && valued {
				outside++
			}
		}
	}

	return outside, nil
}

// crop_values_from_source checks sampled cells against the source itself, when
// the source is already in the grid's projection so a cell centre maps to a
// source pixel arithmetically. A cell whose centre falls in an empty source
// pixel may be empty or filled by the resampling; any other cell must hold the
// source value.
func crop_values_from_source(src, base, out filename, resample string) (float64, error) {
	// Only nearest keeps a cell centre equal to one source pixel.
	if resample != "near" {
		return 1, nil
	}

	s, err := gdal.Open(src, gdal.ReadOnly)
	if err != nil {
		return 0, err
	}
	defer s.Close()

	b, err := gdal.Open(base, gdal.ReadOnly)
	if err != nil {
		return 0, err
	}
	defer b.Close()

	o, err := gdal.Open(out, gdal.ReadOnly)
	if err != nil {
		return 0, err
	}
	defer o.Close()

	if !projections_equal(s.Projection(), b.Projection()) {
		return 1, nil
	}

	values, ok := sample_grid(o)
	if !ok {
		return 1, nil
	}

	sb := s.RasterBand(1)
	source_nodata, source_has := sb.NoDataValue()
	sgt := s.GeoTransform()
	sw, sh := s.RasterXSize(), s.RasterYSize()
	source := make([]float64, sw*sh)
	if err := sb.IO(gdal.Read, 0, 0, sw, sh, source, sw, sh, 0, 0); err != nil {
		return 0, err
	}

	const samples = 2000
	const step = 7 // coprimes with typical widths, to walk the grid

	gt := b.GeoTransform()
	bw, bh := b.RasterXSize(), b.RasterYSize()

	checked, got := 0, 0

	for i := 0; i < bw*bh && checked < samples; i += step {
		r, c := (i/bw)%bh, i%bw
		v, valued := values(r, c)
		if !valued {
			continue
		}

		x := gt[0] + (float64(c)+0.5)*gt[1]
		y := gt[3] + (float64(r)+0.5)*gt[5]

		sc := int(math.Floor((x - sgt[0]) / sgt[1]))
		sr := int(math.Floor((y - sgt[3]) / sgt[5]))
		if sc < 0 || sc >= sw || sr < 0 || sr >= sh {
			continue
		}

		// Nearest may take either pixel whose area holds the centre, so a value
		// from the 3x3 neighbourhood counts as the source's.
		matched := false
		for dr := -1; dr <= 1 && !matched; dr++ {
			for dc := -1; dc <= 1 && !matched; dc++ {
				r2, c2 := sr+dr, sc+dc
				if r2 < 0 || r2 >= sh || c2 < 0 || c2 >= sw {
					continue
				}
				sv := source[r2*sw+c2]
				if math.IsNaN(sv) || (source_has && sv == source_nodata) {
					continue
				}
				matched = v == sv
			}
		}

		checked++
		if matched {
			got++
		}
	}

	if checked < 100 {
		return 1, nil
	}

	return float64(got) / float64(checked), nil
}

// crop_retains_coverage: the rebuild publishes at least as much of the
// geography as the file it replaces. Only cells inside the outline raster's
// mask count -- a cutline used to keep cells outside it, which the grid fix
// deliberately drops.
func crop_retains_coverage(published, base, out filename) error {
	p, err := gdal.Open(published, gdal.ReadOnly)
	if err != nil {
		return err
	}
	defer p.Close()

	b, err := gdal.Open(base, gdal.ReadOnly)
	if err != nil {
		return err
	}
	defer b.Close()

	o, err := gdal.Open(out, gdal.ReadOnly)
	if err != nil {
		return err
	}
	defer o.Close()

	if !projections_equal(p.Projection(), b.Projection()) {
		return nil
	}

	inside, err := valued_inside(o, b)
	if err != nil {
		return err
	}

	before, err := valued_inside(p, b)
	if err != nil {
		return err
	}

	if before > 0 && float64(inside) < 0.95*float64(before) {
		return disagrees("%d cells carry a value inside the outline, the published file had %d", inside, before)
	}

	return nil
}

// valued_inside counts ds's cells that carry a value and fall inside base's
// mask. Both are read on their own grids: a cell centre maps to a base pixel
// arithmetically because the projections match.
func valued_inside(ds, base gdal.Dataset) (int, error) {
	band := ds.RasterBand(1)
	nodata, has := band.NoDataValue()

	bb := base.RasterBand(1)
	base_nodata, base_has := bb.NoDataValue()
	bgt := base.GeoTransform()
	bw, bh := base.RasterXSize(), base.RasterYSize()

	mask := make([]float64, bw*bh)
	if err := bb.IO(gdal.Read, 0, 0, bw, bh, mask, bw, bh, 0, 0); err != nil {
		return 0, err
	}

	width, height := ds.RasterXSize(), ds.RasterYSize()
	gt := ds.GeoTransform()

	values := make([]float64, width*height)
	if err := band.IO(gdal.Read, 0, 0, width, height, values, width, height, 0, 0); err != nil {
		return 0, err
	}

	count := 0
	for r := 0; r < height; r++ {
		y := gt[3] + (float64(r)+0.5)*gt[5]
		for c := 0; c < width; c++ {
			v := values[r*width+c]
			if math.IsNaN(v) || (has && v == nodata) {
				continue
			}

			x := gt[0] + (float64(c)+0.5)*gt[1]
			bc := int(math.Floor((x - bgt[0]) / bgt[1]))
			br := int(math.Floor((y - bgt[3]) / bgt[5]))
			if bc < 0 || bc >= bw || br < 0 || br >= bh {
				continue
			}

			m := mask[br*bw+bc]
			if math.IsNaN(m) || (base_has && m == base_nodata) {
				continue
			}

			count++
		}
	}

	return count, nil
}

func valued_cells(ds gdal.Dataset) (int, error) {
	band := ds.RasterBand(1)
	nodata, has := band.NoDataValue()
	width, height := ds.RasterXSize(), ds.RasterYSize()

	const rows = 256
	valued := 0

	for y := 0; y < height; y += rows {
		n := min(rows, height-y)
		values := make([]float64, width*n)
		if err := band.IO(gdal.Read, 0, y, width, n, values, width, n, 0, 0); err != nil {
			return 0, err
		}
		for _, v := range values {
			if !math.IsNaN(v) && !(has && v == nodata) {
				valued++
			}
		}
	}

	return valued, nil
}

// sample_grid reads band 1 and returns a lookup that reports emptiness by the
// band's nodata value.
func sample_grid(ds gdal.Dataset) (func(r, c int) (float64, bool), bool) {
	band := ds.RasterBand(1)
	nodata, has := band.NoDataValue()
	width, height := ds.RasterXSize(), ds.RasterYSize()

	values := make([]float64, width*height)
	if err := band.IO(gdal.Read, 0, 0, width, height, values, width, height, 0, 0); err != nil {
		return nil, false
	}

	return func(r, c int) (float64, bool) {
		v := values[r*width+c]
		return v, !math.IsNaN(v) && !(has && v == nodata)
	}, true
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
